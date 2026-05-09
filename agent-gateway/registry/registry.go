package registry

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"gopkg.in/yaml.v3"
)

//go:embed all:builtin
var builtinFS embed.FS

// EventType describes a watcher event.
type EventType string

const (
	EventAdded   EventType = "added"
	EventUpdated EventType = "updated"
	EventDeleted EventType = "deleted"
)

// Event is emitted by Watch when a profile YAML in the override dir changes.
type Event struct {
	Type    EventType
	Name    string // profile name
	Profile *Profile
	Err     error // populated on validation failure (Type still emitted)
}

// Registry holds profiles loaded from the embedded built-ins plus zero or
// more override directories (last write wins).
type Registry struct {
	mu       sync.RWMutex
	profiles map[string]*Profile
	overrideDir string
}

// New constructs an empty registry.
func New() *Registry {
	return &Registry{profiles: map[string]*Profile{}}
}

// LoadBuiltins reads every YAML file from the embedded builtin/ tree and
// merges the profiles into the registry. Existing profiles with the same
// name are overwritten; calling LoadBuiltins twice is idempotent.
func (r *Registry) LoadBuiltins() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return fs.WalkDir(builtinFS, "builtin", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".yaml") && !strings.HasSuffix(path, ".yml") {
			return nil
		}
		raw, err := builtinFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read builtin %s: %w", path, err)
		}
		p, err := decodeProfile(raw)
		if err != nil {
			return fmt.Errorf("builtin %s: %w", path, err)
		}
		p.Source = "builtin:" + filepath.Base(path)
		r.profiles[p.Name] = p
		return nil
	})
}

// LoadOverrides reads every *.yaml under dir and merges on top of whatever
// is currently registered. Missing dir is not an error.
func (r *Registry) LoadOverrides(dir string) error {
	if dir == "" {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.overrideDir = dir
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read overrides %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yaml") && !strings.HasSuffix(name, ".yml") {
			continue
		}
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read override %s: %w", path, err)
		}
		p, err := decodeProfile(raw)
		if err != nil {
			return fmt.Errorf("override %s: %w", path, err)
		}
		p.Source = "override:" + path
		r.profiles[p.Name] = p
	}
	return nil
}

// Get fetches a profile by name.
func (r *Registry) Get(name string) (*Profile, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.profiles[name]
	return p, ok
}

// List returns a snapshot sorted by name for stable enumeration.
func (r *Registry) List() []*Profile {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Profile, 0, len(r.profiles))
	for _, p := range r.profiles {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Add inserts or updates a profile in-memory. Used by tests and the
// /v1/agents POST handler. Returns ErrInvalidProfile on validation failure.
func (r *Registry) Add(p *Profile) error {
	if err := p.Validate(); err != nil {
		return fmt.Errorf("invalid profile: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.profiles[p.Name] = p
	return nil
}

// Remove drops a profile by name. Returns false if absent.
func (r *Registry) Remove(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.profiles[name]; !ok {
		return false
	}
	delete(r.profiles, name)
	return true
}

// Watch starts an fsnotify watcher on the override directory. Events are
// emitted on the returned channel. Closing ctx stops the watcher and
// closes the channel.
func (r *Registry) Watch(ctx context.Context) (<-chan Event, error) {
	r.mu.RLock()
	dir := r.overrideDir
	r.mu.RUnlock()
	if dir == "" {
		ch := make(chan Event)
		close(ch)
		return ch, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir override dir: %w", err)
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("fsnotify: %w", err)
	}
	if err := w.Add(dir); err != nil {
		_ = w.Close()
		return nil, fmt.Errorf("watch %s: %w", dir, err)
	}
	out := make(chan Event, 16)
	go func() {
		defer close(out)
		defer w.Close()
		// Debounce: rapid editor writes can fire 4-5 events for a single
		// save. Coalesce per-file.
		debounce := map[string]*time.Timer{}
		var debounceMu sync.Mutex
		fire := func(path string) {
			debounceMu.Lock()
			defer debounceMu.Unlock()
			if t, ok := debounce[path]; ok {
				t.Stop()
			}
			debounce[path] = time.AfterFunc(75*time.Millisecond, func() {
				r.applyOverrideFile(path, out)
			})
		}
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if !strings.HasSuffix(ev.Name, ".yaml") && !strings.HasSuffix(ev.Name, ".yml") {
					continue
				}
				if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) != 0 {
					fire(ev.Name)
				} else if ev.Op&fsnotify.Remove != 0 {
					name := strings.TrimSuffix(filepath.Base(ev.Name), filepath.Ext(ev.Name))
					if r.Remove(name) {
						out <- Event{Type: EventDeleted, Name: name}
					}
				}
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				out <- Event{Type: EventUpdated, Err: err}
			}
		}
	}()
	return out, nil
}

func (r *Registry) applyOverrideFile(path string, out chan<- Event) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			if r.Remove(name) {
				out <- Event{Type: EventDeleted, Name: name}
			}
			return
		}
		out <- Event{Type: EventUpdated, Err: err}
		return
	}
	p, err := decodeProfile(raw)
	if err != nil {
		out <- Event{Type: EventUpdated, Err: fmt.Errorf("%s: %w", path, err)}
		return
	}
	p.Source = "override:" + path
	r.mu.Lock()
	_, existed := r.profiles[p.Name]
	r.profiles[p.Name] = p
	r.mu.Unlock()
	if existed {
		out <- Event{Type: EventUpdated, Name: p.Name, Profile: p}
	} else {
		out <- Event{Type: EventAdded, Name: p.Name, Profile: p}
	}
}

func decodeProfile(raw []byte) (*Profile, error) {
	var p Profile
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}
