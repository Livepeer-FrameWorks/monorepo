// Package datamigrate is the shared library for service-owned background data
// migrations. Each adopting service registers its migrations at startup,
// stores per-job and per-scope state in two tables that this package owns,
// and exposes the standard CLI surface through this package's handlers.
//
// Empty-state contract: an empty Registry() is valid; callers that consume
// catalog-required migrations must surface a NotRegisteredError distinctly
// from "registered but pending" — never report safety when the foundation
// has not been adopted by the relevant service.
package datamigrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"path"
	"sort"
	"strings"
	"sync"
)

// DB is the database surface passed to migration code. *sql.DB and *sql.Tx
// both satisfy it; dry-runs receive a read-only transaction.
type DB interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Migration is one declared service-owned data migration. Run is the worker
// loop; Verify is an optional read-only assertion that the result is correct.
type Migration struct {
	ID                  string
	Service             string
	IntroducedIn        string
	RequiredBeforePhase string // postdeploy | contract
	Description         string

	Run    func(ctx context.Context, db DB, opts RunOptions) (Progress, error)
	Verify func(ctx context.Context, db DB) error
	Scopes func(ctx context.Context, db DB) ([]ScopeKey, error)
}

// ScopeKey partitions a migration's work. Whole-job migrations use the zero
// value; per-tenant migrations set Kind="tenant" and Value=<uuid>.
type ScopeKey struct {
	Kind  string
	Value string
}

// IsZero reports whether this is the whole-job scope (no partition).
func (s ScopeKey) IsZero() bool { return s.Kind == "" && s.Value == "" }

// RunOptions controls one Run invocation. The library calls Run repeatedly
// with the same options until Progress.Done is true; the worker is
// responsible for honoring BatchSize and resuming from its checkpoint.
type RunOptions struct {
	BatchSize  int
	DryRun     bool
	Scope      ScopeKey
	Checkpoint json.RawMessage
}

// Progress is what the worker reports back to the library at each batch
// boundary. The library persists Checkpoint so a later Run can resume.
type Progress struct {
	Scanned    int64
	Changed    int64
	Skipped    int64
	Errors     int64
	Checkpoint json.RawMessage
	Done       bool
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Migration{}
)

const AdoptionMarkerDir = "/etc/frameworks/data-migrations"

// AdoptionMarkerPath is the remote marker the cluster CLI checks before it
// invokes a service's data-migrations subcommand.
func AdoptionMarkerPath(service string) string {
	name := strings.TrimSpace(service)
	if name == "" {
		name = "unknown"
	}
	return path.Join(AdoptionMarkerDir, name+".enabled")
}

// Register adds a migration to the process-wide registry. Services call this
// from init() or service bootstrap. Re-registration of the same id panics —
// IDs must be unique across the binary.
func Register(m Migration) {
	if m.ID == "" {
		panic("datamigrate.Register: empty ID")
	}
	if m.Service == "" {
		panic("datamigrate.Register: empty Service for " + m.ID)
	}
	if m.Run == nil {
		panic("datamigrate.Register: nil Run for " + m.ID)
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	if _, exists := registry[m.ID]; exists {
		panic("datamigrate.Register: duplicate ID " + m.ID)
	}
	registry[m.ID] = m
}

// Registry returns every registered migration, sorted by (Service, IntroducedIn, ID).
func Registry() []Migration {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]Migration, 0, len(registry))
	for _, m := range registry {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		if out[i].IntroducedIn != out[j].IntroducedIn {
			return out[i].IntroducedIn < out[j].IntroducedIn
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Lookup returns the registered migration with id, or nil.
func Lookup(id string) *Migration {
	registryMu.RLock()
	defer registryMu.RUnlock()
	if m, ok := registry[id]; ok {
		return &m
	}
	return nil
}

// ByService returns every registered migration owned by service.
func ByService(service string) []Migration {
	registryMu.RLock()
	defer registryMu.RUnlock()
	var out []Migration
	for _, m := range registry {
		if m.Service == service {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IntroducedIn != out[j].IntroducedIn {
			return out[i].IntroducedIn < out[j].IntroducedIn
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// resetForTest clears the registry. Tests only.
func resetForTest() {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry = map[string]Migration{}
}
