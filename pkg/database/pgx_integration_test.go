package database_test

import (
	"context"
	"database/sql"
	"os"
	"reflect"
	"testing"

	fwdb "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/lib/pq"
)

// These tests validate the lib/pq -> yugabyte/pgx driver swap against a real
// PostgreSQL/YugabyteDB instance. They are the Phase-0 contract gate for the
// array-codec migration: they prove bind-direct slices and fwdb.ArrayScan
// round-trip, and that fwdb.SQLState reads the SQLSTATE from a *pgconn.PgError.
// Skipped unless PGX_IT_DSN points at a database (e.g. a throwaway container).
func openIT(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("PGX_IT_DSN")
	if dsn == "" {
		t.Skip("PGX_IT_DSN not set; skipping pgx integration test")
	}
	// Go through the real Connect path so the test exercises exec-mode injection
	// (withPgxExecMode) and the pool/ping config, not just a bare sql.Open.
	cfg := fwdb.DefaultConfig()
	cfg.URL = dsn
	db, err := fwdb.Connect(cfg, logging.NewLogger())
	if err != nil {
		t.Fatalf("fwdb.Connect: %v", err)
	}
	return db
}

func TestPgxArrayRoundTrip(t *testing.T) {
	db := openIT(t)
	defer db.Close()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		CREATE TEMP TABLE pgx_arr_it (
			id        bigint PRIMARY KEY,
			tags      text[]   NOT NULL,
			ids       uuid[]   NOT NULL,
			counts    bigint[] NOT NULL
		)`); err != nil {
		t.Fatalf("create temp table: %v", err)
	}

	wantTags := []string{"a", "b", "c"}
	wantIDs := []string{"11111111-1111-1111-1111-111111111111", "22222222-2222-2222-2222-222222222222"}
	wantCounts := []int64{1, 2, 3}

	// BIND: pass Go slices directly (no pq.Array wrapper); pgx stdlib encodes them.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO pgx_arr_it (id, tags, ids, counts) VALUES ($1, $2, $3, $4)`,
		int64(1), wantTags, wantIDs, wantCounts,
	); err != nil {
		t.Fatalf("insert with direct slice bind: %v", err)
	}

	// SCAN: decode via fwdb.ArrayScan into Go slices.
	var gotTags, gotIDs []string
	var gotCounts []int64
	if err := db.QueryRowContext(ctx,
		`SELECT tags, ids, counts FROM pgx_arr_it WHERE id = $1`, int64(1),
	).Scan(fwdb.ArrayScan(&gotTags), fwdb.ArrayScan(&gotIDs), fwdb.ArrayScan(&gotCounts)); err != nil {
		t.Fatalf("scan via ArrayScan: %v", err)
	}

	if !reflect.DeepEqual(gotTags, wantTags) {
		t.Errorf("tags: got %v want %v", gotTags, wantTags)
	}
	if len(gotIDs) != len(wantIDs) {
		t.Errorf("ids len: got %v want %v", gotIDs, wantIDs)
	}
	if !reflect.DeepEqual(gotCounts, wantCounts) {
		t.Errorf("counts: got %v want %v", gotCounts, wantCounts)
	}
}

// TestPgxRetainedPqArrayBind exercises the bind path production actually keeps:
// pq.Array / pq.StringArray (driver.Valuers) as query args under the pgx driver.
// The other array test binds direct slices; this one locks the retained shape so
// the "keep pq.Array for binds" decision is covered by the real driver, not just
// reasoning/sqlmock.
func TestPgxRetainedPqArrayBind(t *testing.T) {
	db := openIT(t)
	defer db.Close()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx,
		`CREATE TEMP TABLE pgx_pqbind_it (id bigint, tags text[], counts bigint[])`); err != nil {
		t.Fatalf("create temp table: %v", err)
	}

	wantTags := []string{"x", "y"}
	wantCounts := []int64{7, 8, 9}
	// pq.StringArray and pq.Array are both driver.Valuers emitting the {...} form.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO pgx_pqbind_it (id, tags, counts) VALUES ($1, $2, $3)`,
		int64(1), pq.StringArray(wantTags), pq.Array(wantCounts),
	); err != nil {
		t.Fatalf("pq.Array/pq.StringArray bind under pgx: %v", err)
	}

	var gotTags []string
	var gotCounts []int64
	if err := db.QueryRowContext(ctx,
		`SELECT tags, counts FROM pgx_pqbind_it WHERE id = $1`, int64(1),
	).Scan(fwdb.ArrayScan(&gotTags), fwdb.ArrayScan(&gotCounts)); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !reflect.DeepEqual(gotTags, wantTags) || !reflect.DeepEqual(gotCounts, wantCounts) {
		t.Fatalf("roundtrip mismatch: tags=%v counts=%v", gotTags, gotCounts)
	}
}

func TestPgxSQLStateUniqueViolation(t *testing.T) {
	db := openIT(t)
	defer db.Close()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx,
		`CREATE TEMP TABLE pgx_uv_it (k text PRIMARY KEY)`); err != nil {
		t.Fatalf("create temp table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO pgx_uv_it (k) VALUES ('x')`); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO pgx_uv_it (k) VALUES ('x')`)
	if err == nil {
		t.Fatal("expected unique violation, got nil")
	}
	if code := fwdb.SQLState(err); code != "23505" {
		t.Fatalf("SQLState = %q, want 23505 (err=%v)", code, err)
	}
}
