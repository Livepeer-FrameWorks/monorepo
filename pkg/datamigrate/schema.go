package datamigrate

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

// SchemaSQL is the table set every adopting service installs in its own
// database. Two tables: _data_migrations is the per-job lifecycle (one row
// per migration ID), _data_migration_runs is per-scope progress + lease
// state. Whole-job migrations use the empty scope (”,”).
//
// This file is NOT placed under pkg/database/sql/schema/ because that
// directory's filenames define database names — adding _data_migrations.sql
// there would invent a phantom database called "_data_migrations".
const SchemaSQL = `
CREATE TABLE IF NOT EXISTS _data_migrations (
    id              TEXT PRIMARY KEY,
    release_version TEXT NOT NULL,
    status          TEXT NOT NULL,
    started_at      TIMESTAMPTZ,
    completed_at    TIMESTAMPTZ,
    last_error      TEXT,
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS _data_migration_runs (
    id               TEXT NOT NULL REFERENCES _data_migrations(id),
    scope_kind       TEXT NOT NULL DEFAULT '',
    scope_value      TEXT NOT NULL DEFAULT '',
    status           TEXT NOT NULL,
    checkpoint       JSONB NOT NULL DEFAULT '{}',
    lease_owner      TEXT,
    lease_expires_at TIMESTAMPTZ,
    attempt_count    INT NOT NULL DEFAULT 0,
    scanned_count    BIGINT NOT NULL DEFAULT 0,
    changed_count    BIGINT NOT NULL DEFAULT 0,
    skipped_count    BIGINT NOT NULL DEFAULT 0,
    error_count      BIGINT NOT NULL DEFAULT 0,
    last_error       TEXT,
    started_at       TIMESTAMPTZ,
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    completed_at     TIMESTAMPTZ,
    PRIMARY KEY (id, scope_kind, scope_value)
);
`

// EnsureSchema serializes ledger DDL on its locked database session. IF NOT
// EXISTS alone can race an uncommitted CREATE on another replica. A session
// lock also spans DDL on engines without transactional DDL.
func EnsureSchema(ctx context.Context, db *sql.DB) error {
	err := database.WithSessionLockConn(ctx, db, "datamigrate.schema", func(conn *sql.Conn) error {
		_, schemaErr := conn.ExecContext(ctx, SchemaSQL)
		return schemaErr
	})
	if err != nil {
		return fmt.Errorf("datamigrate: ensure schema: %w", err)
	}
	return nil
}
