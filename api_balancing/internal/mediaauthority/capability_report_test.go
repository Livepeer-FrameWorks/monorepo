package mediaauthority

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type capabilityPushRecorder struct {
	mu     sync.Mutex
	pushes []CellPlacementCapability
	err    error
}

func (r *capabilityPushRecorder) push(_ context.Context, capability CellPlacementCapability) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pushes = append(r.pushes, capability)
	return r.err
}

func (r *capabilityPushRecorder) snapshot() []CellPlacementCapability {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]CellPlacementCapability(nil), r.pushes...)
}

func (r *capabilityPushRecorder) setErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// The cell's attestation goes to Commodore on the first beat and on every
// change, an unchanged one at most every five minutes, and a failed push asks
// for a prompt retry.
func TestCapabilityReporter_PushesOnChange(t *testing.T) {
	notReady := CellPlacementCapability{SupportedSchemaVersions: []uint32{1, 2}, LiveReplicas: 2}
	ready := CellPlacementCapability{SupportedSchemaVersions: []uint32{1, 2}, LiveReplicas: 2, EnforcementReady: true}
	current := notReady
	var readErr error
	recorder := &capabilityPushRecorder{}
	reporter := NewCellCapabilityReporter(recorder.push, nil)
	reporter.read = func(context.Context) (CellPlacementCapability, error) { return current, readErr }
	ctx := context.Background()
	start := time.Now()
	beat := func(after time.Duration) bool { return reporter.report(ctx, start.Add(after)) }

	beat(0)
	if len(recorder.pushes) != 1 || recorder.pushes[0].EnforcementReady {
		t.Fatalf("first beat pushed %v, want the startup attestation", recorder.pushes)
	}
	beat(ReplicaHeartbeatInterval)
	if len(recorder.pushes) != 1 {
		t.Fatalf("unchanged attestation pushed again: %v", recorder.pushes)
	}
	current = ready
	beat(2 * ReplicaHeartbeatInterval)
	if len(recorder.pushes) != 2 || !recorder.pushes[1].EnforcementReady {
		t.Fatalf("readiness change was not pushed on the beat that observed it: %v", recorder.pushes)
	}

	// A ledger read that fails attests nothing and is never pushed.
	readErr = errors.New("ledger unavailable")
	current = notReady
	if beat(3*ReplicaHeartbeatInterval) || len(recorder.pushes) != 2 {
		t.Fatalf("failed ledger read was pushed or retried: %v", recorder.pushes)
	}
	readErr = nil

	recorder.err = status.Error(codes.Unavailable, "commodore restarting")
	current = CellPlacementCapability{SupportedSchemaVersions: []uint32{1, 2}, LiveReplicas: 3, EnforcementReady: true}
	if !beat(4 * ReplicaHeartbeatInterval) {
		t.Fatal("failed push did not ask for a retry")
	}
	recorder.err = nil
	if beat(4*ReplicaHeartbeatInterval+capabilityReportRetry) || len(recorder.pushes) != 4 || recorder.pushes[3].LiveReplicas != 3 {
		t.Fatalf("failed push was not retried: %v", recorder.pushes)
	}
	beat(5 * ReplicaHeartbeatInterval)
	if len(recorder.pushes) != 4 {
		t.Fatalf("delivered attestation pushed again: %v", recorder.pushes)
	}
	beat(4*ReplicaHeartbeatInterval + capabilityReportRetry + capabilityReportRefresh)
	if len(recorder.pushes) != 5 {
		t.Fatalf("unchanged attestation was not refreshed after %s: %v", capabilityReportRefresh, recorder.pushes)
	}
}

// A Commodore without the report RPC answers Unimplemented. The cell stops
// pushing every change, since Commodore learns it through its probe, and asks
// again only after the refresh interval.
func TestCapabilityReporter_OldCommodoreFallsBackToProbe(t *testing.T) {
	current := CellPlacementCapability{SupportedSchemaVersions: []uint32{1, 2}, LiveReplicas: 1}
	recorder := &capabilityPushRecorder{err: status.Error(codes.Unimplemented, "unknown method")}
	reporter := NewCellCapabilityReporter(recorder.push, nil)
	reporter.read = func(context.Context) (CellPlacementCapability, error) { return current, nil }
	ctx := context.Background()
	start := time.Now()
	if reporter.report(ctx, start) {
		t.Fatal("Unimplemented asked for a prompt retry")
	}
	current.EnforcementReady = true
	reporter.report(ctx, start.Add(ReplicaHeartbeatInterval))
	reporter.report(ctx, start.Add(2*ReplicaHeartbeatInterval))
	if len(recorder.pushes) != 1 {
		t.Fatalf("pushed %d times to a Commodore without the report RPC, want 1", len(recorder.pushes))
	}
	recorder.err = nil
	reporter.report(ctx, start.Add(capabilityReportRefresh))
	if len(recorder.pushes) != 2 || !recorder.pushes[1].EnforcementReady {
		t.Fatalf("upgraded Commodore was not asked again after %s: %v", capabilityReportRefresh, recorder.pushes)
	}
	reporter.report(ctx, start.Add(capabilityReportRefresh+ReplicaHeartbeatInterval))
	if len(recorder.pushes) != 2 {
		t.Fatalf("unchanged attestation pushed again after the upgrade: %v", recorder.pushes)
	}
}

func TestCapabilityReporter_NilPushDoesNothing(t *testing.T) {
	reporter := NewCellCapabilityReporter(nil, nil)
	reporter.read = func(context.Context) (CellPlacementCapability, error) {
		t.Fatal("ledger read without a push")
		return CellPlacementCapability{}, nil
	}
	if reporter.report(context.Background(), time.Now()) {
		t.Fatal("nil push asked for a retry")
	}
	var nilReporter *CellCapabilityReporter
	nilReporter.Nudge()
}

// expectCapabilityLedger answers the heartbeat write and every ledger read with
// a cell whose live replicas all enforce placement.
func expectCapabilityLedger(mock sqlmock.Sqlmock, beats, reads int) {
	for range beats {
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.control_replicas")).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	for range reads {
		mock.ExpectQuery("live_replicas").WillReturnRows(sqlmock.NewRows([]string{"live_replicas", "min_schema_version", "all_enforced", "min_authority_feature_level"}).AddRow(1, 3, true, 1))
	}
}

// The heartbeat that first records this replica's enforcement also reports the
// cell ready, without waiting for the next heartbeat interval. A report that
// fails because Commodore is not connected yet is retried within seconds, and
// a Commodore connection nudges the report immediately.
func TestCapabilityReporter_HeartbeatReportsReadinessWithinSeconds(t *testing.T) {
	withPlacementEnforced(t, true)
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.MatchExpectationsInOrder(false)
	expectCapabilityLedger(mock, 1, 3)
	store := &Store{db: db}

	recorder := &capabilityPushRecorder{err: status.Error(codes.Unavailable, "commodore is not connected")}
	reporter := NewCellCapabilityReporter(recorder.push, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		store.RunReplicaHeartbeat(ctx, "replica-a", "test", reporter, nil)
	}()
	t.Cleanup(func() { cancel(); <-done })

	waitPushes := func(n int, within time.Duration) []CellPlacementCapability {
		t.Helper()
		deadline := time.Now().Add(within)
		for time.Now().Before(deadline) {
			if pushes := recorder.snapshot(); len(pushes) >= n {
				return pushes
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("got %d capability pushes within %s, want %d", len(recorder.snapshot()), within, n)
		return nil
	}
	if pushes := waitPushes(1, time.Second); !pushes[0].EnforcementReady {
		t.Fatalf("first heartbeat reported %+v, want the cell ready", pushes[0])
	}
	// Commodore is still unreachable: the retry follows within seconds.
	waitPushes(2, capabilityReportRetry+time.Second)
	recorder.setErr(nil)
	reporter.Nudge()
	if pushes := waitPushes(3, time.Second); !pushes[2].EnforcementReady {
		t.Fatalf("nudged report sent %+v, want the cell ready", pushes[2])
	}
}

func TestCellPlacementCapabilityProtoBoundsReplicas(t *testing.T) {
	if (CellPlacementCapability{LiveReplicas: 1 << 21}).Proto() != nil || (CellPlacementCapability{LiveReplicas: -1}).Proto() != nil {
		t.Fatal("unrepresentable replica count produced an attestation")
	}
	got := CellPlacementCapability{SupportedSchemaVersions: []uint32{1, 2}, EnforcementReady: true, LiveReplicas: 2, NodePlacementReady: true, LongValidityReady: true, UseReportsReady: true}.Proto()
	if got.GetLiveReplicas() != 2 || !got.GetEnforcementReady() || !got.GetNodePlacementReady() || !got.GetLongValidityReady() || !got.GetUseReportsReady() || len(got.GetSupportedSchemaVersions()) != 2 {
		t.Fatalf("attestation lost a field: %v", got)
	}
}
