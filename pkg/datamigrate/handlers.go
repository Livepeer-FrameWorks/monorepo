package datamigrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// HandleArgv is the standard entry point an adopting service wires into
// main(). It parses the data-migrations subcommand and dispatches. Returns
// the special error ErrNotDataMigrationsCommand when args[0] is not
// "data-migrations" so callers can fall through to their regular startup.
//
// Adopting services typically write:
//
//	if err := datamigrate.HandleArgv(ctx, openDB, os.Stdout, os.Args[1:]); err == nil {
//	    return
//	} else if !errors.Is(err, datamigrate.ErrNotDataMigrationsCommand) {
//	    log.Fatal(err)
//	}
//
// This keeps pkg/datamigrate framework-free (no Cobra import) while still
// giving every adopting service the same argv contract for SSH invocation.
func HandleArgv(ctx context.Context, openDB func() (*sql.DB, error), out io.Writer, args []string) error {
	if len(args) == 0 || args[0] != "data-migrations" {
		return ErrNotDataMigrationsCommand
	}
	if len(args) < 2 {
		return printSubcommandUsage(out)
	}

	sub := args[1]
	rest := args[2:]
	switch sub {
	case "list":
		return HandleList(out, rest)
	case "status":
		return HandleStatus(ctx, openDB, out, rest)
	case "run":
		return HandleRun(ctx, openDB, out, rest)
	case "verify":
		return HandleVerify(ctx, openDB, out, rest)
	case "pause":
		return HandlePause(ctx, openDB, out, rest)
	case "resume":
		return HandleResume(ctx, openDB, out, rest)
	case "help", "-h", "--help":
		return printSubcommandUsage(out)
	default:
		return fmt.Errorf("unknown data-migrations subcommand %q", sub)
	}
}

// ErrNotDataMigrationsCommand is returned by HandleArgv when args[0] is not
// "data-migrations" — callers fall through to their regular startup.
var ErrNotDataMigrationsCommand = errors.New("not a data-migrations command")

func printSubcommandUsage(out io.Writer) error {
	fmt.Fprintln(out, `Usage: <service> data-migrations <subcommand> [flags]

Subcommands:
  list                                List registered migrations in this binary
  status <id> [--format text|json]    Show persisted state for one migration
  run <id> [--batch-size N] [--scope-kind K --scope-value V] [--dry-run]
                                      Run one migration to completion
  verify <id>                         Run the migration's read-only verification
  pause <id>                          Mark a migration paused
  resume <id>                         Mark a paused migration runnable again`)
	return nil
}

// HandleList prints registered migrations. Args: [--format text|json]
func HandleList(out io.Writer, args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	format := fs.String("format", "text", "Output format: text|json")
	if err := fs.Parse(args); err != nil {
		return err
	}

	migs := Registry()
	if *format == "json" {
		return writeJSON(out, registryJSONList(migs))
	}
	if len(migs) == 0 {
		fmt.Fprintln(out, "no data migrations registered in this binary")
		return nil
	}
	for _, m := range migs {
		fmt.Fprintf(out, "%s  service=%s  introduced_in=%s  required_before=%s\n",
			m.ID, m.Service, m.IntroducedIn, m.RequiredBeforePhase)
	}
	return nil
}

// HandleStatus prints persisted state for one id. Args: <id> [--format ...]
func HandleStatus(ctx context.Context, openDB func() (*sql.DB, error), out io.Writer, args []string) error {
	id, rest, err := requirePositional("status", args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	format := fs.String("format", "text", "Output format: text|json")
	if parseErr := fs.Parse(rest); parseErr != nil {
		return parseErr
	}

	db, err := openDB()
	if err != nil {
		return fmt.Errorf("open service db: %w", err)
	}
	queryCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	job, err := LoadJob(queryCtx, db, id)
	if err != nil {
		if IsNotRegistered(err) {
			if *format == "json" {
				return writeJSON(out, map[string]any{"id": id, "status": "not_registered", "not_registered": true})
			}
			fmt.Fprintf(out, "%s NOT REGISTERED in this binary\n", id)
			return nil
		}
		return err
	}
	runs, err := LoadRuns(queryCtx, db, id)
	if err != nil {
		return err
	}

	if *format == "json" {
		return writeJSON(out, jobJSON(job, runs, registryServiceFor(id)))
	}
	fmt.Fprintf(out, "%s  service=%s  status=%s\n", job.ID, registryServiceFor(id), job.Status)
	for _, r := range runs {
		fmt.Fprintf(out, "  scope=%s/%s  status=%s  scanned=%d changed=%d errors=%d\n",
			r.Scope.Kind, r.Scope.Value, r.Status, r.Scanned, r.Changed, r.Errors)
	}
	if len(runs) == 0 {
		fmt.Fprintln(out, "  (no runs recorded)")
	}
	return nil
}

// HandleRun runs a registered migration to completion or until error.
func HandleRun(ctx context.Context, openDB func() (*sql.DB, error), out io.Writer, args []string) error {
	id, rest, err := requirePositional("run", args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	batchSize := fs.Int("batch-size", 1000, "Batch size hint")
	dryRun := fs.Bool("dry-run", false, "Run inside a read-only database transaction")
	scopeKind := fs.String("scope-kind", "", "Scope partition kind (tenant, shard, ...)")
	scopeValue := fs.String("scope-value", "", "Scope partition value")
	if parseErr := fs.Parse(rest); parseErr != nil {
		return parseErr
	}

	m := Lookup(id)
	if m == nil {
		return fmt.Errorf("data migration %q not registered in this binary", id)
	}
	db, err := openDB()
	if err != nil {
		return fmt.Errorf("open service db: %w", err)
	}
	scope := ScopeKey{Kind: *scopeKind, Value: *scopeValue}
	if scope.Kind == "" && scope.Value != "" {
		return fmt.Errorf("--scope-kind is required when --scope-value is set")
	}

	scopes := []ScopeKey{scope}
	if scope.IsZero() && m.Scopes != nil {
		scopeDB := DB(db)
		var tx *sql.Tx
		if *dryRun {
			tx, err = db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if err != nil {
				return fmt.Errorf("start read-only scope-discovery transaction: %w", err)
			}
			defer tx.Rollback() //nolint:errcheck // dry-run never commits
			scopeDB = tx
		}
		discovered, scopesErr := m.Scopes(ctx, scopeDB)
		if scopesErr != nil {
			return fmt.Errorf("discover scopes for %q: %w", id, scopesErr)
		}
		if len(discovered) == 0 {
			return fmt.Errorf("data migration %q has no scopes to run", id)
		}
		scopes = discovered
	}

	if *dryRun {
		for _, s := range scopes {
			if runErr := runDryScope(ctx, db, out, m, id, s, *batchSize); runErr != nil {
				return runErr
			}
		}
		return nil
	}

	if schemaErr := EnsureSchema(ctx, db); schemaErr != nil {
		return schemaErr
	}
	job, err := LoadJob(ctx, db, id)
	if err != nil && !IsNotRegistered(err) {
		return err
	}
	if err == nil && job.Status == StatusCompleted {
		fmt.Fprintf(out, "%s already completed\n", id)
		return nil
	}
	if markErr := MarkJobRunning(ctx, db, id, m.IntroducedIn); markErr != nil {
		return markErr
	}
	for _, s := range scopes {
		if upsertErr := UpsertRun(ctx, db, id, s); upsertErr != nil {
			return upsertErr
		}
	}

	for _, s := range scopes {
		if runErr := runScope(ctx, db, out, m, id, s, *batchSize, *dryRun); runErr != nil {
			return runErr
		}
	}
	completed, err := MarkJobCompletedIfAllRunsCompleted(ctx, db, id)
	if err != nil {
		return err
	}
	if completed {
		fmt.Fprintf(out, "%s completed\n", id)
	} else {
		fmt.Fprintf(out, "%s scope(s) completed; job remains running until all known scopes complete\n", id)
	}
	return nil
}

func runScope(ctx context.Context, db *sql.DB, out io.Writer, m *Migration, id string, scope ScopeKey, batchSize int, dryRun bool) error {
	if dryRun {
		return runDryScope(ctx, db, out, m, id, scope, batchSize)
	}

	runState, err := LoadRun(ctx, db, id, scope)
	if err != nil {
		return err
	}
	if runState.Status == StatusCompleted {
		fmt.Fprintf(out, "%s/%s already completed\n", id, scope)
		return nil
	}

	owner := fmt.Sprintf("%s-pid%d", m.Service, os.Getpid())
	ttl := 2 * time.Minute
	ok, err := Acquire(ctx, db, id, scope, owner, ttl)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("could not acquire lease for %q/%s (held by another worker or paused)", id, scope)
	}
	defer func() {
		_ = Release(context.Background(), db, id, scope, owner) //nolint:errcheck // best-effort lease release on exit
	}()

	checkpoint := runState.Checkpoint
	for {
		prog, runErr := m.Run(ctx, db, RunOptions{BatchSize: batchSize, DryRun: dryRun, Scope: scope, Checkpoint: checkpoint})
		if runErr != nil {
			_ = MarkRunFailed(context.Background(), db, id, scope, runErr) //nolint:errcheck // best-effort failure record
			_ = MarkJobFailed(context.Background(), db, id, runErr)        //nolint:errcheck // best-effort failure record
			return runErr
		}
		if cpErr := Checkpoint(ctx, db, id, scope, prog); cpErr != nil {
			return cpErr
		}
		checkpoint = prog.Checkpoint
		if prog.Done {
			if err := MarkRunCompleted(ctx, db, id, scope); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s/%s completed: scanned=%d changed=%d errors=%d\n",
				id, scope, prog.Scanned, prog.Changed, prog.Errors)
			return nil
		}
		if _, hbErr := Heartbeat(ctx, db, id, scope, owner, ttl); hbErr != nil {
			return hbErr
		}
	}
}

func runDryScope(ctx context.Context, db *sql.DB, out io.Writer, m *Migration, id string, scope ScopeKey, batchSize int) error {
	checkpoint, err := dryRunCheckpoint(ctx, db, id, scope)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("start read-only dry-run transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // dry-run never commits

	prog, err := m.Run(ctx, tx, RunOptions{BatchSize: batchSize, DryRun: true, Scope: scope, Checkpoint: checkpoint})
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s/%s dry-run: scanned=%d changed=%d skipped=%d errors=%d done=%v\n",
		id, scope, prog.Scanned, prog.Changed, prog.Skipped, prog.Errors, prog.Done)
	return nil
}

func dryRunCheckpoint(ctx context.Context, db *sql.DB, id string, scope ScopeKey) (json.RawMessage, error) {
	runState, err := LoadRun(ctx, db, id, scope)
	if err == nil {
		if runState.Checkpoint != nil {
			return runState.Checkpoint, nil
		}
		return json.RawMessage(`{}`), nil
	}
	if errors.Is(err, sql.ErrNoRows) || isMissingDataMigrationState(err) {
		return json.RawMessage(`{}`), nil
	}
	return nil, err
}

func isMissingDataMigrationState(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "_data_migration") &&
		(strings.Contains(msg, "does not exist") || strings.Contains(msg, "no such table"))
}

// HandleVerify runs the migration's Verify hook.
func HandleVerify(ctx context.Context, openDB func() (*sql.DB, error), out io.Writer, args []string) error {
	id, _, err := requirePositional("verify", args)
	if err != nil {
		return err
	}
	m := Lookup(id)
	if m == nil {
		return fmt.Errorf("data migration %q not registered in this binary", id)
	}
	if m.Verify == nil {
		return fmt.Errorf("data migration %q has no Verify function", id)
	}
	db, err := openDB()
	if err != nil {
		return fmt.Errorf("open service db: %w", err)
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("start read-only verify transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // verify never commits
	if err := m.Verify(ctx, tx); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s verified\n", id)
	return nil
}

// HandlePause sets job status to paused.
func HandlePause(ctx context.Context, openDB func() (*sql.DB, error), out io.Writer, args []string) error {
	id, _, err := requirePositional("pause", args)
	if err != nil {
		return err
	}
	m := Lookup(id)
	if m == nil {
		return &NotRegisteredError{ID: id}
	}
	db, err := openDB()
	if err != nil {
		return err
	}
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := EnsureSchema(queryCtx, db); err != nil {
		return err
	}
	if err := PauseJob(queryCtx, db, id, m.IntroducedIn); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s paused\n", id)
	return nil
}

// HandleResume sets job status back to pending.
func HandleResume(ctx context.Context, openDB func() (*sql.DB, error), out io.Writer, args []string) error {
	id, _, err := requirePositional("resume", args)
	if err != nil {
		return err
	}
	db, err := openDB()
	if err != nil {
		return err
	}
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := EnsureSchema(queryCtx, db); err != nil {
		return err
	}
	if err := ResumeJob(queryCtx, db, id); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s resumed\n", id)
	return nil
}

func requirePositional(sub string, args []string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("data-migrations %s: missing required <id>", sub)
	}
	return args[0], args[1:], nil
}

func writeJSON(out io.Writer, v any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

type registryEntry struct {
	ID                  string `json:"id"`
	Service             string `json:"service"`
	IntroducedIn        string `json:"introduced_in"`
	RequiredBeforePhase string `json:"required_before_phase"`
	Description         string `json:"description"`
}

func registryJSONList(migs []Migration) []registryEntry {
	out := make([]registryEntry, 0, len(migs))
	for _, m := range migs {
		out = append(out, registryEntry{m.ID, m.Service, m.IntroducedIn, m.RequiredBeforePhase, m.Description})
	}
	return out
}

type runJSON struct {
	ScopeKind   string     `json:"scope_kind"`
	ScopeValue  string     `json:"scope_value"`
	Status      Status     `json:"status"`
	Scanned     int64      `json:"scanned"`
	Changed     int64      `json:"changed"`
	Skipped     int64      `json:"skipped"`
	Errors      int64      `json:"errors"`
	LastError   string     `json:"last_error,omitempty"`
	Attempts    int        `json:"attempts"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	UpdatedAt   time.Time  `json:"updated_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
}

type jobJSONOut struct {
	ID             string     `json:"id"`
	Service        string     `json:"service"`
	ReleaseVersion string     `json:"release_version"`
	Status         Status     `json:"status"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastError      string     `json:"last_error,omitempty"`
	Runs           []runJSON  `json:"runs"`
}

func jobJSON(job JobState, runs []RunState, service string) jobJSONOut {
	out := jobJSONOut{
		ID:             job.ID,
		Service:        service,
		ReleaseVersion: job.ReleaseVersion,
		Status:         job.Status,
		StartedAt:      job.StartedAt,
		CompletedAt:    job.CompletedAt,
		UpdatedAt:      job.UpdatedAt,
		LastError:      job.LastError,
		Runs:           make([]runJSON, 0, len(runs)),
	}
	for _, r := range runs {
		out.Runs = append(out.Runs, runJSON{
			ScopeKind:   r.Scope.Kind,
			ScopeValue:  r.Scope.Value,
			Status:      r.Status,
			Scanned:     r.Scanned,
			Changed:     r.Changed,
			Skipped:     r.Skipped,
			Errors:      r.Errors,
			LastError:   r.LastError,
			Attempts:    r.AttemptCount,
			StartedAt:   r.StartedAt,
			UpdatedAt:   r.UpdatedAt,
			CompletedAt: r.CompletedAt,
		})
	}
	return out
}

func registryServiceFor(id string) string {
	if m := Lookup(id); m != nil {
		return m.Service
	}
	return ""
}
