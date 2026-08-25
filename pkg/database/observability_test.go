package database

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/yugabyte/pgx/v5/pgconn"
)

type databaseTimeoutError struct{}

func (databaseTimeoutError) Error() string { return "network timeout" }
func (databaseTimeoutError) Timeout() bool { return true }

func TestClassifyDatabaseError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		scan bool
		want string
	}{
		{name: "nil", want: ""},
		{name: "undefined table", err: &pgconn.PgError{Code: "42P01"}, want: FailureUndefinedObject},
		{name: "undefined column", err: &pgconn.PgError{Code: "42703"}, want: FailureUndefinedObject},
		{name: "constraint", err: &pgconn.PgError{Code: "23505"}, want: FailureConstraint},
		{name: "invalid input", err: &pgconn.PgError{Code: "22P02"}, want: FailureTypeMismatch},
		{name: "clickhouse identifier", err: errors.New("DB::Exception: Unknown identifier missing_column"), want: FailureUndefinedObject},
		{name: "scan", err: errors.New("sql: Scan error on column index 2"), scan: true, want: FailureScan},
		{name: "operational", err: errors.New("connection timeout"), want: ""},
		{name: "operational scan", err: errors.New("connection timeout"), scan: true, want: ""},
		{name: "deadline scan", err: context.DeadlineExceeded, scan: true, want: ""},
		{name: "capability", err: &CapabilityError{Service: "purser", Engine: EnginePostgres, Capability: "usage", Err: errors.New("broken")}, want: FailureCapabilityMismatch},
		{name: "capability deadline", err: &CapabilityError{Service: "periscope-query", Engine: EngineClickHouse, Capability: "viewer facts", Err: context.DeadlineExceeded}, want: ""},
		{name: "capability canceled", err: &CapabilityError{Service: "purser", Engine: EnginePostgres, Capability: "usage", Err: context.Canceled}, want: ""},
		{name: "capability network timeout", err: &CapabilityError{Service: "periscope-query", Engine: EngineClickHouse, Capability: "viewer facts", Err: databaseTimeoutError{}}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClassifyDatabaseError(tt.err, tt.scan); got != tt.want {
				t.Fatalf("ClassifyDatabaseError() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestObserveDatabaseErrorRecordsOnlyContractFailures(t *testing.T) {
	counter := databaseFailures.WithLabelValues("observability-test", string(EnginePostgres), FailureUndefinedObject)
	before := testutil.ToFloat64(counter)
	ObserveDatabaseError("observability-test", EnginePostgres, &pgconn.PgError{Code: "42703"}, false)
	if got := testutil.ToFloat64(counter); got != before+1 {
		t.Fatalf("undefined-object counter = %v, want %v", got, before+1)
	}

	operational := databaseFailures.WithLabelValues("observability-test", string(EnginePostgres), FailureConstraint)
	before = testutil.ToFloat64(operational)
	ObserveDatabaseError("observability-test", EnginePostgres, errors.New("connection timeout"), false)
	if got := testutil.ToFloat64(operational); got != before {
		t.Fatalf("operational error changed contract counter from %v to %v", before, got)
	}
}
