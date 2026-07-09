package database_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	fwdb "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
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

// TestPgxJSONBBind is the contract gate for JSONB parameter binding under the
// pgx stdlib driver: []byte wire-encodes as bytea and jsonb rejects it, so all
// jsonb binds must go through fwdb.JSONText (or bind a string). BYTEA columns
// must keep receiving []byte.
func TestPgxJSONBBind(t *testing.T) {
	db := openIT(t)
	defer db.Close()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx,
		`CREATE TEMP TABLE pgx_jsonb_it (id bigint PRIMARY KEY, doc jsonb, blob bytea)`); err != nil {
		t.Fatalf("create temp table: %v", err)
	}

	payload := []byte(`{"a":1,"b":["x","y"]}`)

	// The regression this test guards against: raw []byte -> jsonb fails.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO pgx_jsonb_it (id, doc) VALUES (1, $1)`, payload); err == nil {
		t.Fatal("raw []byte bind to jsonb unexpectedly succeeded; revisit the JSONText contract")
	}

	// The contract: JSONText for jsonb, raw []byte for bytea, in one statement.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO pgx_jsonb_it (id, doc, blob) VALUES (1, $1::jsonb, $2)`,
		fwdb.JSONText(payload), payload); err != nil {
		t.Fatalf("JSONText bind: %v", err)
	}

	// nil marshal output must bind NULL (COALESCE($n::jsonb, ...) sites depend on it).
	if _, err := db.ExecContext(ctx,
		`INSERT INTO pgx_jsonb_it (id, doc) VALUES (2, $1)`, fwdb.JSONText(nil)); err != nil {
		t.Fatalf("JSONText(nil) bind: %v", err)
	}

	var docIsNull bool
	var gotDoc, gotBlob []byte
	if err := db.QueryRowContext(ctx,
		`SELECT doc, blob, (SELECT doc IS NULL FROM pgx_jsonb_it WHERE id = 2)
		 FROM pgx_jsonb_it WHERE id = 1`).Scan(&gotDoc, &gotBlob, &docIsNull); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !docIsNull {
		t.Error("JSONText(nil) did not bind NULL")
	}
	if !reflect.DeepEqual(gotBlob, payload) {
		t.Errorf("bytea round-trip: got %q want %q", gotBlob, payload)
	}
	var got map[string]any
	if err := json.Unmarshal(gotDoc, &got); err != nil || got["a"] != float64(1) {
		t.Errorf("jsonb round-trip: got %s (err=%v)", gotDoc, err)
	}
}

// TestPgxJSONBValuerRoundTrip locks the models JSON Valuers to the string
// contract: Value() returning []byte would bind as bytea and break jsonb.
func TestPgxJSONBValuerRoundTrip(t *testing.T) {
	db := openIT(t)
	defer db.Close()
	ctx := context.Background()

	if _, err := db.ExecContext(ctx,
		`CREATE TEMP TABLE pgx_jsonb_valuer_it (id bigint PRIMARY KEY, doc jsonb)`); err != nil {
		t.Fatalf("create temp table: %v", err)
	}
	in := models.JSONB{"limit": float64(5), "tags": []any{"a"}}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO pgx_jsonb_valuer_it (id, doc) VALUES (1, $1), (2, $2)`,
		in, models.JSONB(nil)); err != nil {
		t.Fatalf("models.JSONB bind: %v", err)
	}
	var out models.JSONB
	var nilIsNull bool
	if err := db.QueryRowContext(ctx,
		`SELECT doc, (SELECT doc IS NULL FROM pgx_jsonb_valuer_it WHERE id = 2)
		 FROM pgx_jsonb_valuer_it WHERE id = 1`).Scan(&out, &nilIsNull); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if !reflect.DeepEqual(out, in) {
		t.Errorf("round-trip: got %v want %v", out, in)
	}
	if !nilIsNull {
		t.Error("nil models.JSONB did not bind NULL")
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
