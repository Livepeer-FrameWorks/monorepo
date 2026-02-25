package knowledge

import (
	"context"
	"database/sql"
	"fmt"
)

// EnsureEmbeddingDimensions checks whether the embedding vector column matches
// the target dimension count. When they differ it truncates stale data, alters
// the column type, and rebuilds the HNSW index.
// Returns true when a migration was performed.
func EnsureEmbeddingDimensions(ctx context.Context, db *sql.DB, target int) (bool, error) {
	if target <= 0 {
		return false, fmt.Errorf("invalid embedding dimensions: %d", target)
	}

	// pgvector stores the dimension count in atttypmod for vector(N) columns.
	var current int
	err := db.QueryRowContext(ctx, `
		SELECT atttypmod
		FROM pg_attribute
		WHERE attrelid = 'skipper.skipper_knowledge'::regclass
		  AND attname = 'embedding'
	`).Scan(&current)
	if err != nil {
		return false, fmt.Errorf("query current embedding dimensions: %w", err)
	}

	if current == target {
		return false, nil
	}

	// Dimensions changed — old embeddings are from a different model and
	// cannot be meaningfully searched, so we truncate before altering.
	stmts := []string{
		`DROP INDEX IF EXISTS skipper.skipper_knowledge_embedding_idx`,
		`TRUNCATE skipper.skipper_knowledge`,
		fmt.Sprintf(`ALTER TABLE skipper.skipper_knowledge ALTER COLUMN embedding TYPE vector(%d)`, target),
		`CREATE INDEX skipper_knowledge_embedding_idx ON skipper.skipper_knowledge USING hnsw (embedding vector_cosine_ops) WITH (m = 24, ef_construction = 256)`,
	}
	for _, stmt := range stmts {
		if _, execErr := db.ExecContext(ctx, stmt); execErr != nil {
			return false, fmt.Errorf("migrate embedding dimensions (%d → %d): %w", current, target, execErr)
		}
	}

	return true, nil
}
