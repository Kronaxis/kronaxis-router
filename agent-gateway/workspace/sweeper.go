package workspace

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// Sweep walks the root and removes any direct child directory whose mtime
// is older than maxAge. Used at gateway startup to clean up stale
// ephemeral workspaces left over from a crash.
//
// It does NOT descend further than one level: the convention is that each
// request gets a single direct child of the root. This keeps the sweeper
// fast and avoids accidentally removing user data if root is mis-configured.
func Sweep(ctx context.Context, root string, maxAge time.Duration) (removed int, err error) {
	if root == "" {
		return 0, nil
	}
	st, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if !st.IsDir() {
		return 0, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		select {
		case <-ctx.Done():
			return removed, ctx.Err()
		default:
		}
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(root, e.Name())
		fi, ferr := os.Stat(p)
		if ferr != nil {
			continue
		}
		if fi.ModTime().Before(cutoff) {
			if rerr := os.RemoveAll(p); rerr == nil {
				removed++
			}
		}
	}
	return removed, nil
}
