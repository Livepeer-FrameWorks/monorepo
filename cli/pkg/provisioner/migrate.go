package provisioner

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	dbsql "frameworks/pkg/database/sql"

	_ "github.com/lib/pq"
)

// Migration represents a single versioned SQL migration file.
type Migration struct {
	Version  string // e.g. "v1.1.0"
	Sequence int    // parsed from NNN prefix
	Filename string // e.g. "001_purser_add_invoice_field.sql"
	Path     string // full embed path
	Checksum string // SHA-256 of content
	content  string
}

// MigrationResult records a single applied migration.
type MigrationResult struct {
	Migration
	AppliedAt time.Time
}

const migrationsTrackingDDL = `CREATE TABLE IF NOT EXISTS _migrations (
	version    TEXT NOT NULL,
	seq        INT NOT NULL,
	filename   TEXT NOT NULL,
	checksum   TEXT NOT NULL,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	PRIMARY KEY (version, seq)
)`

// RunPostgresMigrations applies any pending versioned migrations from the
// embedded filesystem to the target Postgres database.
// If dryRun is true it returns the list of pending migrations without applying.
func RunPostgresMigrations(ctx context.Context, connStr string, dryRun bool) ([]MigrationResult, error) {
	all, err := discoverMigrations("migrations")
	if err != nil {
		return nil, fmt.Errorf("discover migrations: %w", err)
	}
	if len(all) == 0 {
		return nil, nil
	}

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	defer db.Close()

	if _, execErr := db.ExecContext(ctx, migrationsTrackingDDL); execErr != nil {
		return nil, fmt.Errorf("ensure tracking table: %w", execErr)
	}

	applied, err := loadApplied(ctx, db)
	if err != nil {
		return nil, err
	}

	pending := filterPending(all, applied)
	if len(pending) == 0 || dryRun {
		results := make([]MigrationResult, len(pending))
		for i, m := range pending {
			results[i] = MigrationResult{Migration: m}
		}
		return results, nil
	}

	var results []MigrationResult
	for _, m := range pending {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return results, fmt.Errorf("begin tx for %s/%s: %w", m.Version, m.Filename, err)
		}
		if _, err := tx.ExecContext(ctx, m.content); err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				return results, fmt.Errorf("apply %s/%s: %w (rollback also failed: %w)", m.Version, m.Filename, err, rbErr)
			}
			return results, fmt.Errorf("apply %s/%s: %w", m.Version, m.Filename, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO _migrations (version, seq, filename, checksum) VALUES ($1, $2, $3, $4)`,
			m.Version, m.Sequence, m.Filename, m.Checksum,
		); err != nil {
			if rbErr := tx.Rollback(); rbErr != nil {
				return results, fmt.Errorf("record %s/%s: %w (rollback also failed: %w)", m.Version, m.Filename, err, rbErr)
			}
			return results, fmt.Errorf("record %s/%s: %w", m.Version, m.Filename, err)
		}
		if err := tx.Commit(); err != nil {
			return results, fmt.Errorf("commit %s/%s: %w", m.Version, m.Filename, err)
		}
		results = append(results, MigrationResult{Migration: m, AppliedAt: time.Now()})
	}
	return results, nil
}

// discoverMigrations walks the embedded FS under root looking for
// versioned migration directories (e.g. migrations/v1.0.0/001_foo.sql).
func discoverMigrations(root string) ([]Migration, error) {
	var out []Migration
	err := fs.WalkDir(dbsql.Content, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".sql") {
			return err
		}
		// Expected: root/vX.Y.Z/NNN_description.sql
		dir := path.Dir(p)
		ver := path.Base(dir)
		if !strings.HasPrefix(ver, "v") {
			return nil
		}
		base := path.Base(p)
		seq := parseSequence(base)

		data, readErr := dbsql.Content.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(data))

		out = append(out, Migration{
			Version:  ver,
			Sequence: seq,
			Filename: base,
			Path:     p,
			Checksum: checksum,
			content:  string(data),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Version != out[j].Version {
			return compareSemver(out[i].Version, out[j].Version) < 0
		}
		return out[i].Sequence < out[j].Sequence
	})
	return out, nil
}

func parseSequence(filename string) int {
	// NNN_description.sql -> NNN
	idx := strings.Index(filename, "_")
	if idx <= 0 {
		return 0
	}
	var n int
	n, _ = strconv.Atoi(filename[:idx]) //nolint:errcheck // best-effort parse, returns 0 on failure
	return n
}

func loadApplied(ctx context.Context, db *sql.DB) (map[string]struct{}, error) {
	rows, err := db.QueryContext(ctx, `SELECT version, seq FROM _migrations`)
	if err != nil {
		return nil, fmt.Errorf("load applied: %w", err)
	}
	defer rows.Close()

	set := make(map[string]struct{})
	for rows.Next() {
		var v string
		var s int
		if err := rows.Scan(&v, &s); err != nil {
			return nil, err
		}
		set[fmt.Sprintf("%s:%d", v, s)] = struct{}{}
	}
	return set, rows.Err()
}

func filterPending(all []Migration, applied map[string]struct{}) []Migration {
	var pending []Migration
	for _, m := range all {
		key := fmt.Sprintf("%s:%d", m.Version, m.Sequence)
		if _, ok := applied[key]; !ok {
			pending = append(pending, m)
		}
	}
	return pending
}

// compareSemver compares two version strings like "v1.2.3".
// Returns -1 if a < b, 0 if equal, 1 if a > b.
// Falls back to lexicographic comparison on parse failure.
func compareSemver(a, b string) int {
	parseVer := func(s string) [3]int {
		s = strings.TrimPrefix(s, "v")
		parts := strings.SplitN(s, ".", 3)
		var v [3]int
		for i := 0; i < len(parts) && i < 3; i++ {
			v[i], _ = strconv.Atoi(parts[i]) //nolint:errcheck // best-effort parse, returns 0 on failure
		}
		return v
	}
	va, vb := parseVer(a), parseVer(b)
	for i := 0; i < 3; i++ {
		if va[i] < vb[i] {
			return -1
		}
		if va[i] > vb[i] {
			return 1
		}
	}
	return 0
}
