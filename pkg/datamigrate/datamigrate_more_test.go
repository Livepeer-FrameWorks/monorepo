package datamigrate

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestRegistry_OrderingTieBreaks(t *testing.T) {
	resetForTest()
	noop := func(_ context.Context, _ DB, _ RunOptions) (Progress, error) {
		return Progress{Done: true}, nil
	}
	// Two services; within "alpha" two share an IntroducedIn so the ID
	// tie-break decides; "beta" comes after by Service. Registration order
	// is intentionally scrambled.
	Register(Migration{ID: "z", Service: "alpha", IntroducedIn: "v0.4.0", Run: noop})
	Register(Migration{ID: "b", Service: "beta", IntroducedIn: "v0.1.0", Run: noop})
	Register(Migration{ID: "m", Service: "alpha", IntroducedIn: "v0.4.0", Run: noop})
	Register(Migration{ID: "a", Service: "alpha", IntroducedIn: "v0.3.0", Run: noop})

	all := Registry()
	wantIDs := []string{"a", "m", "z", "b"}
	if len(all) != len(wantIDs) {
		t.Fatalf("got %d migrations, want %d", len(all), len(wantIDs))
	}
	for i, want := range wantIDs {
		if all[i].ID != want {
			t.Fatalf("Registry order[%d]=%q want %q (full=%v)", i, all[i].ID, want, ids(all))
		}
	}
}

func TestByService_OrderingTieBreaks(t *testing.T) {
	resetForTest()
	noop := func(_ context.Context, _ DB, _ RunOptions) (Progress, error) {
		return Progress{Done: true}, nil
	}
	Register(Migration{ID: "z", Service: "alpha", IntroducedIn: "v0.4.0", Run: noop})
	Register(Migration{ID: "m", Service: "alpha", IntroducedIn: "v0.4.0", Run: noop})
	Register(Migration{ID: "a", Service: "alpha", IntroducedIn: "v0.3.0", Run: noop})
	Register(Migration{ID: "other", Service: "beta", IntroducedIn: "v0.1.0", Run: noop})

	got := ByService("alpha")
	wantIDs := []string{"a", "m", "z"}
	if len(got) != len(wantIDs) {
		t.Fatalf("ByService(alpha) got %d, want %d", len(got), len(wantIDs))
	}
	for i, want := range wantIDs {
		if got[i].ID != want {
			t.Fatalf("ByService order[%d]=%q want %q (full=%v)", i, got[i].ID, want, ids(got))
		}
	}
}

func ids(ms []Migration) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}

func TestPreDeployBlockers_EmptyRBVDefaultGate(t *testing.T) {
	src := staticSource(t, map[string]LiveStatus{
		"purser.x": {ID: "x", Service: "purser", Status: StatusPending},
	})

	// Empty RequiredBeforeVersion: the default gate blocks only when target is
	// strictly past IntroducedIn. A migration introduced AFTER the target must
	// not block; one introduced BEFORE the target must block.
	tests := []struct {
		name         string
		introducedIn string
		wantBlocked  bool
	}{
		{"introduced_after_target_not_blocked", "v0.6.0", false},
		{"introduced_before_target_blocked", "v0.4.0", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqs := []Requirement{
				{ID: "x", Service: "purser", IntroducedIn: tt.introducedIn},
			}
			got, err := PreDeployBlockers(context.Background(), src, reqs, "v0.3.0", "v0.5.0", trivialSemver)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			blocked := len(got) > 0
			if blocked != tt.wantBlocked {
				t.Fatalf("introducedIn=%s blocked=%v want %v (got %+v)", tt.introducedIn, blocked, tt.wantBlocked, got)
			}
		})
	}
}

func TestHandleRun_ScopeValueRequiresKind(t *testing.T) {
	resetForTest()
	Register(Migration{
		ID:           "scoped",
		Service:      "purser",
		IntroducedIn: "v0.5.0",
		Run: func(_ context.Context, _ DB, _ RunOptions) (Progress, error) {
			return Progress{Done: true}, nil
		},
	})
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	var out bytes.Buffer
	err = HandleRun(context.Background(), func() (*sql.DB, error) { return db, nil }, &out,
		[]string{"scoped", "--scope-value", "tenant-1"})
	if err == nil {
		t.Fatal("expected error: --scope-value without --scope-kind")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("scope-kind")) {
		t.Fatalf("expected scope-kind error, got %v", err)
	}
}

func TestLoadRun_Success(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	cols := []string{
		"id", "scope_kind", "scope_value", "status", "checkpoint",
		"lease_owner", "lease_expires_at", "attempt_count",
		"scanned_count", "changed_count", "skipped_count", "error_count",
		"last_error", "started_at", "updated_at", "completed_at",
	}
	rows := sqlmock.NewRows(cols).AddRow(
		"job1", "tenant", "t-1", string(StatusRunning), []byte(`{"cur":5}`),
		nil, nil, 2,
		int64(100), int64(40), int64(3), int64(1),
		"", nil, time.Now(), nil,
	)
	mock.ExpectQuery("_data_migration_runs").
		WithArgs("job1", "tenant", "t-1").
		WillReturnRows(rows)

	got, err := LoadRun(context.Background(), db, "job1", ScopeKey{Kind: "tenant", Value: "t-1"})
	if err != nil {
		t.Fatalf("LoadRun returned error: %v", err)
	}
	if got.ID != "job1" || got.Status != StatusRunning || got.Scanned != 100 ||
		got.Changed != 40 || got.AttemptCount != 2 {
		t.Fatalf("unexpected RunState: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestLoadRun_ScanError(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("_data_migration_runs").
		WithArgs("job1", "", "").
		WillReturnError(errors.New("connection reset"))

	_, err = LoadRun(context.Background(), db, "job1", ScopeKey{})
	if err == nil {
		t.Fatal("expected error from failed scan")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("connection reset")) {
		t.Fatalf("expected wrapped scan error, got %v", err)
	}
}

func TestHandleRun_DryRunRunError(t *testing.T) {
	resetForTest()
	Register(Migration{
		ID:           "failing",
		Service:      "purser",
		IntroducedIn: "v0.5.0",
		Run: func(_ context.Context, _ DB, _ RunOptions) (Progress, error) {
			return Progress{}, errors.New("worker exploded")
		},
	})
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("_data_migration_runs").
		WithArgs("failing", "", "").
		WillReturnError(errors.New(`pq: relation "_data_migration_runs" does not exist`))
	mock.ExpectBegin()
	mock.ExpectRollback()

	var out bytes.Buffer
	err = HandleRun(context.Background(), func() (*sql.DB, error) { return db, nil }, &out,
		[]string{"failing", "--dry-run"})
	if err == nil {
		t.Fatal("expected dry-run to surface the Run error")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("worker exploded")) {
		t.Fatalf("expected worker error, got %v", err)
	}
}

func TestDryRunCheckpoint_ExistingCheckpoint(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	cols := []string{
		"id", "scope_kind", "scope_value", "status", "checkpoint",
		"lease_owner", "lease_expires_at", "attempt_count",
		"scanned_count", "changed_count", "skipped_count", "error_count",
		"last_error", "started_at", "updated_at", "completed_at",
	}
	rows := sqlmock.NewRows(cols).AddRow(
		"job1", "", "", string(StatusRunning), []byte(`{"cur":7}`),
		nil, nil, 1,
		int64(10), int64(2), int64(0), int64(0),
		"", nil, time.Now(), nil,
	)
	mock.ExpectQuery("_data_migration_runs").
		WithArgs("job1", "", "").
		WillReturnRows(rows)

	cp, err := dryRunCheckpoint(context.Background(), db, "job1", ScopeKey{})
	if err != nil {
		t.Fatalf("dryRunCheckpoint error: %v", err)
	}
	if !bytes.Contains(cp, []byte(`"cur":7`)) {
		t.Fatalf("expected existing checkpoint, got %s", string(cp))
	}
}
