package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// graphifyIngest walks roots, chunks each file, embeds, upserts to kr_chunks.
// Idempotent: rows are upserted on (source_path, chunk_idx).
//
// Concurrency: roots are walked in parallel; chunked files flow into a buffered
// channel; N workers (Concurrency) pull batches of BatchSize chunks, embed,
// and upsert. The embedder is the typical bottleneck (single-threaded
// sentence-transformers behind a Flask sidecar); concurrent walkers + upserts
// still hide ~half of the total wall time vs full serial.
//
// Excludes: hidden dirs (`.git`, `.venv`, `node_modules`), files matching
// `excludes`, files larger than 4 MiB, binaries.
type IngestOpts struct {
	Roots       []string
	Excludes    []string
	BatchSize   int
	Concurrency int
	Verbose     bool
}

type IngestStats struct {
	FilesScanned   int64
	FilesIngested  int64
	FilesSkipped   int64
	ChunksWritten  int64
	BytesProcessed int64
	StartedAt      time.Time
	FinishedAt     time.Time
}

func graphifyIngest(ctx context.Context, db *sql.DB, emb Embedder, opts IngestOpts) (*IngestStats, error) {
	if opts.BatchSize <= 0 {
		opts.BatchSize = 32
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}
	stats := &IngestStats{StartedAt: time.Now()}

	defaultExcludes := []string{
		".git", ".venv", "node_modules", "vendor", "graphify-out",
		"__pycache__", "dist", "build", ".next", ".cache",
		".terraform", "target", "bin", "obj",
	}
	excludes := append(defaultExcludes, opts.Excludes...)

	chunkCh := make(chan []Chunk, opts.Concurrency*2)
	errCh := make(chan error, opts.Concurrency+1)

	// Worker pool: pulls chunks, batches, embeds, upserts.
	var workerWG sync.WaitGroup
	for i := 0; i < opts.Concurrency; i++ {
		workerWG.Add(1)
		go func(workerID int) {
			defer workerWG.Done()
			buf := make([]Chunk, 0, opts.BatchSize)
			flush := func() error {
				if len(buf) == 0 {
					return nil
				}
				texts := make([]string, len(buf))
				for i, c := range buf {
					texts[i] = c.Content
				}
				vecs, err := emb.Embed(ctx, "passage", texts)
				if err != nil {
					return fmt.Errorf("worker %d embed: %w", workerID, err)
				}
				if len(vecs) != len(buf) {
					return fmt.Errorf("worker %d: embed returned %d vectors for %d chunks",
						workerID, len(vecs), len(buf))
				}
				if err := graphifyUpsertBatch(ctx, db, buf, vecs); err != nil {
					return fmt.Errorf("worker %d upsert: %w", workerID, err)
				}
				atomic.AddInt64(&stats.ChunksWritten, int64(len(buf)))
				buf = buf[:0]
				return nil
			}
			for chunks := range chunkCh {
				// Cap each flush at BatchSize. A single file can produce
				// hundreds of chunks; sending all 200+ in one /embed POST
				// pushes against the embedder's per-request budget. Splitting
				// here keeps individual POSTs under the configured batch size
				// regardless of file size.
				for len(chunks) > 0 {
					room := opts.BatchSize - len(buf)
					if room > len(chunks) {
						room = len(chunks)
					}
					buf = append(buf, chunks[:room]...)
					chunks = chunks[room:]
					if len(buf) >= opts.BatchSize {
						if err := flush(); err != nil {
							errCh <- err
							// Drain remaining work so the producer doesn't block.
							for range chunkCh {
							}
							return
						}
					}
				}
			}
			if err := flush(); err != nil {
				errCh <- err
			}
		}(i)
	}

	// Producer: walk roots in parallel, push chunks into chunkCh.
	var walkWG sync.WaitGroup
	for _, root := range opts.Roots {
		walkWG.Add(1)
		go func(root string) {
			defer walkWG.Done()
			err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if info.IsDir() {
					name := info.Name()
					for _, ex := range excludes {
						if name == ex {
							return filepath.SkipDir
						}
					}
					if strings.HasPrefix(name, ".") && name != "." {
						return filepath.SkipDir
					}
					return nil
				}
				atomic.AddInt64(&stats.FilesScanned, 1)
				if !shouldIngestFile(path) {
					atomic.AddInt64(&stats.FilesSkipped, 1)
					return nil
				}
				chunks, err := chunkFile(path)
				if err != nil || len(chunks) == 0 {
					atomic.AddInt64(&stats.FilesSkipped, 1)
					return nil
				}
				atomic.AddInt64(&stats.FilesIngested, 1)
				atomic.AddInt64(&stats.BytesProcessed, info.Size())

				mtime := info.ModTime()
				ext := strings.TrimPrefix(filepath.Ext(path), ".")
				for i := range chunks {
					if chunks[i].Metadata == nil {
						chunks[i].Metadata = map[string]any{}
					}
					chunks[i].Metadata["file_mtime"] = mtime.UTC().Format(time.RFC3339)
					chunks[i].Metadata["ext"] = ext
				}
				if opts.Verbose {
					fmt.Printf("queued %s (%d chunks)\n", path, len(chunks))
				}
				select {
				case chunkCh <- chunks:
				case <-ctx.Done():
					return ctx.Err()
				}
				return nil
			})
			if err != nil && err != context.Canceled {
				errCh <- fmt.Errorf("walk %s: %w", root, err)
			}
		}(root)
	}

	walkWG.Wait()
	close(chunkCh)
	workerWG.Wait()
	close(errCh)

	stats.FinishedAt = time.Now()
	for err := range errCh {
		if err != nil {
			return stats, err
		}
	}
	return stats, nil
}

func shouldIngestFile(path string) bool {
	base := filepath.Base(path)
	if strings.HasPrefix(base, ".") {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".mdx", ".txt", ".rst",
		".go", ".py", ".ts", ".tsx", ".js", ".jsx",
		".rs", ".java", ".c", ".h", ".cc", ".cpp",
		".rb", ".php", ".sh", ".bash", ".zsh",
		".yaml", ".yml", ".toml", ".json", ".sql",
		".html", ".css", ".scss":
		return true
	}
	if filepath.Ext(path) == "" {
		if info, err := os.Stat(path); err == nil && info.Size() < 64*1024 {
			lower := strings.ToLower(base)
			if strings.Contains(lower, "readme") || strings.Contains(lower, "license") || strings.Contains(lower, "changelog") {
				return true
			}
		}
	}
	return false
}

func graphifyUpsertBatch(ctx context.Context, db *sql.DB, chunks []Chunk, vecs [][]float32) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	const stmt = `
INSERT INTO kr_chunks (source_path, chunk_idx, content, embedding, metadata, source_mtime, ingested_at)
VALUES ($1, $2, $3, $4::vector, $5, $6, NOW())
ON CONFLICT (source_path, chunk_idx) DO UPDATE
SET content = EXCLUDED.content,
    embedding = EXCLUDED.embedding,
    metadata = EXCLUDED.metadata,
    source_mtime = EXCLUDED.source_mtime,
    ingested_at = NOW()`
	for i, c := range chunks {
		mtimeStr, _ := c.Metadata["file_mtime"].(string)
		var mtime time.Time
		if mtimeStr != "" {
			mtime, _ = time.Parse(time.RFC3339, mtimeStr)
		}
		mdJSON, err := json.Marshal(c.Metadata)
		if err != nil {
			return err
		}
		vec := vecs[i]
		if _, err := tx.ExecContext(ctx, stmt,
			c.SourcePath, c.Idx, c.Content, vectorLiteral(vec), string(mdJSON), nullableTime(mtime),
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.Grow(8 + len(v)*8)
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%g", x)
	}
	b.WriteByte(']')
	return b.String()
}

func nullableTime(t time.Time) interface{} {
	if t.IsZero() {
		return nil
	}
	return t
}
