package main

import (
	"context"
	"database/sql"
	"fmt"
)

// graphifyEnsureSchema creates the kr_chunks table + indexes if they don't
// exist. The pgvector dim is captured at first creation; switching embedding
// models with a different dim requires `kronaxis-router graphify reset`.
func graphifyEnsureSchema(ctx context.Context, db *sql.DB, dim int) error {
	if dim <= 0 {
		return fmt.Errorf("graphify: invalid embedding dim %d", dim)
	}
	if _, err := db.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS vector"); err != nil {
		return fmt.Errorf("create extension vector: %w", err)
	}
	createTable := fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS kr_chunks (
    id           BIGSERIAL PRIMARY KEY,
    source_path  TEXT NOT NULL,
    chunk_idx    INT NOT NULL,
    content      TEXT NOT NULL,
    embedding    VECTOR(%d),
    metadata     JSONB DEFAULT '{}'::JSONB,
    source_mtime TIMESTAMPTZ,
    ingested_at  TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(source_path, chunk_idx)
)`, dim)
	if _, err := db.ExecContext(ctx, createTable); err != nil {
		return fmt.Errorf("create kr_chunks: %w", err)
	}

	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS kr_chunks_embedding_hnsw ON kr_chunks USING hnsw (embedding vector_cosine_ops) WITH (m = 16, ef_construction = 64)`,
		`CREATE INDEX IF NOT EXISTS kr_chunks_content_gin ON kr_chunks USING GIN (to_tsvector('english', content))`,
		`CREATE INDEX IF NOT EXISTS kr_chunks_source_path ON kr_chunks (source_path)`,
		`CREATE INDEX IF NOT EXISTS kr_chunks_metadata ON kr_chunks USING GIN (metadata)`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create index: %w (%s)", err, stmt)
		}
	}
	return nil
}

// graphifyTableDim returns the dim of the existing kr_chunks.embedding column,
// or 0 if the table doesn't exist yet.
func graphifyTableDim(ctx context.Context, db *sql.DB) (int, error) {
	var atttypmod sql.NullInt64
	row := db.QueryRowContext(ctx, `
SELECT atttypmod
FROM pg_attribute
WHERE attrelid = 'kr_chunks'::regclass AND attname = 'embedding'`)
	if err := row.Scan(&atttypmod); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil
		}
		// table doesn't exist
		return 0, nil
	}
	if !atttypmod.Valid {
		return 0, nil
	}
	return int(atttypmod.Int64), nil
}

// graphifyResetSchema drops the kr_chunks table. Used when the operator
// changes embedding dim. Idempotent.
func graphifyResetSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, "DROP TABLE IF EXISTS kr_chunks CASCADE")
	return err
}
