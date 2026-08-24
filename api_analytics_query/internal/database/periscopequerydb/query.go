package periscopequerydb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	pkgdatabase "github.com/Livepeer-FrameWorks/monorepo/pkg/database"
)

var observerService atomic.Pointer[string]

func init() {
	service := "periscope-query"
	observerService.Store(&service)
}

// SetObserverService selects the process identity attached to shared query
// contract metrics. Periscope Query and Metering are separate binaries that
// intentionally execute this same catalog; call once during process startup.
func SetObserverService(service string) {
	if service == "" {
		panic("periscopequerydb: observer service is required")
	}
	observerService.Store(&service)
}

func observedService() string {
	service := observerService.Load()
	if service == nil {
		return "periscope-query"
	}
	return *service
}

// DBTX is the database/sql read surface used by Periscope Query. Keeping the
// interface here lets production, transactions, and contract doubles execute
// the exact same service-owned statements.
type DBTX interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// Statement identifies one audited ClickHouse read shape.
type Statement struct {
	name string
	sql  string
}

func statement(name, query string) Statement {
	if name == "" || query == "" {
		panic("periscopequerydb: statement name and SQL are required")
	}
	return Statement{name: name, sql: query}
}

// Name is the stable contract identifier used by real-engine verification.
func (s Statement) Name() string { return s.name }

// SQL returns the exact runtime text executed by this statement.
func (s Statement) SQL() string { return s.sql }

// Query executes a multi-row statement without changing the caller's scan or
// pagination lifecycle.
func (s Statement) Query(ctx context.Context, db DBTX, args ...any) (*Rows, error) {
	return Query(ctx, db, s.sql, args...)
}

// QueryRow executes a single-row statement without changing its scan boundary.
func (s Statement) QueryRow(ctx context.Context, db DBTX, args ...any) *Row {
	return QueryRow(ctx, db, s.sql, args...)
}

type traceContextKey struct{}

// Trace records the stable names of query shapes reached by a real service
// contract. It is scoped to a context and has no production-global state.
type Trace struct {
	mu     sync.Mutex
	names  map[string]int
	errors []error
}

// Errors returns scan failures observed even when a caller skips a malformed row.
func (t *Trace) Errors() []error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]error(nil), t.errors...)
}

// WithTrace attaches an empty query trace to a context.
func WithTrace(ctx context.Context) (context.Context, *Trace) {
	trace := &Trace{names: map[string]int{}}
	return context.WithValue(ctx, traceContextKey{}, trace), trace
}

func recordError(ctx context.Context, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	pkgdatabase.ObserveDatabaseError(observedService(), pkgdatabase.EngineClickHouse, err, true)
	trace, ok := ctx.Value(traceContextKey{}).(*Trace)
	if !ok || trace == nil || err == nil {
		return
	}
	trace.mu.Lock()
	trace.errors = append(trace.errors, err)
	trace.mu.Unlock()
}

// Rows preserves database/sql iteration while tracing result-shape failures.
type Rows struct {
	*sql.Rows
	ctx context.Context
}

func (r *Rows) Scan(dest ...any) error {
	err := r.Rows.Scan(dest...)
	recordError(r.ctx, err)
	return err
}

// Row preserves database/sql single-row behavior while tracing scan failures.
type Row struct {
	*sql.Row
	ctx context.Context
}

func (r *Row) Scan(dest ...any) error {
	err := r.Row.Scan(dest...)
	recordError(r.ctx, err)
	return err
}

// Names returns the number of times each named query site executed.
func (t *Trace) Names() map[string]int {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[string]int, len(t.names))
	for name, count := range t.names {
		out[name] = count
	}
	return out
}

func record(ctx context.Context, query string) {
	trace, ok := ctx.Value(traceContextKey{}).(*Trace)
	if !ok || trace == nil {
		return
	}
	_, file, line, _ := runtime.Caller(2)
	name := fmt.Sprintf("%s:%d:%s", filepath.Base(file), line, Fingerprint(query))
	trace.mu.Lock()
	trace.names[name]++
	trace.mu.Unlock()
}

// Fingerprint returns the stable audit identity of a rendered query shape.
func Fingerprint(query string) string {
	normalized := strings.Join(strings.Fields(query), " ")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(normalized)))

}

// Query executes one auditable multi-row ClickHouse read shape.
func Query(ctx context.Context, db DBTX, query string, args ...any) (*Rows, error) {
	if query == "" {
		panic("periscopequerydb: SQL is required")
	}
	record(ctx, query)
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		pkgdatabase.ObserveDatabaseError(observedService(), pkgdatabase.EngineClickHouse, err, false)
		return nil, err
	}
	return &Rows{Rows: rows, ctx: ctx}, nil
}

// QueryRow executes one auditable single-row ClickHouse read shape.
func QueryRow(ctx context.Context, db DBTX, query string, args ...any) *Row {
	if query == "" {
		panic("periscopequerydb: SQL is required")
	}
	record(ctx, query)
	return &Row{Row: db.QueryRowContext(ctx, query, args...), ctx: ctx}
}
