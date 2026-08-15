package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/jobs"
	"github.com/DATA-DOG/go-sqlmock"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Invariant: SendDVRStart is fire-and-forget, so only an explicit client-error rejection PROVES
// the node did not record. Every transport/availability failure (including ErrNotConnected, which is a
// gRPC Unavailable) is ambiguous — the start MAY have been accepted — and must be left for recovery, not
// terminalized.
func TestClassifyDVRSendError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want dvrSendClass
	}{
		{"nil defensive", nil, dvrSendRecoverable},
		{"not connected (Unavailable)", control.ErrNotConnected, dvrSendRecoverable},
		{"deadline exceeded", status.Error(codes.DeadlineExceeded, "timeout"), dvrSendRecoverable},
		{"unknown", status.Error(codes.Unknown, "?"), dvrSendRecoverable},
		{"internal", status.Error(codes.Internal, "boom"), dvrSendRecoverable},
		{"untyped transport", errors.New("connection reset by peer"), dvrSendRecoverable},
		{"invalid argument", status.Error(codes.InvalidArgument, "bad request"), dvrSendDefinitiveReject},
		{"failed precondition", status.Error(codes.FailedPrecondition, "node not ready"), dvrSendDefinitiveReject},
		{"not found", status.Error(codes.NotFound, "no such node"), dvrSendDefinitiveReject},
	}
	for _, tc := range cases {
		if got := classifyDVRSendError(tc.err); got != tc.want {
			t.Errorf("%s: classifyDVRSendError = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// Invariant: a StartDVR retry that finds a prior attempt's row still
// 'starting' must NOT be optimistically promoted to 'recording'. reconcileStartingDVR re-reads the
// durable status and reports it HONESTLY: only the node's own confirmation (which promotes the row to
// 'recording' via processDVRProgress) makes it "already_started"; a row still 'starting' reports the
// in-flight "starting"; a terminal row surfaces an error.
func TestReconcileStartingDVR_RecordingReportsAlreadyStarted(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	req := &sharedpb.StartDVRRequest{InternalName: "live+s1", TenantId: "tenant-a"}

	// The node's first DVRProgress already promoted the row to 'recording' (positive confirmation).
	mock.ExpectQuery(`SELECT status FROM foghorn\.artifacts`).
		WithArgs("dvr-h", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("recording"))

	st, err := srv.reconcileStartingDVR(context.Background(), req, "dvr-h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st != "already_started" {
		t.Fatalf("expected already_started for a confirmed recording, got %q", st)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant:a row still 'starting' (the node has not yet confirmed) reports the honest
// in-flight "starting" — never a false "already_started"/"started"-as-confirmed. No status write
// happens: the recording transition is driven exclusively by the node's progress report.
func TestReconcileStartingDVR_StillStartingReportsStarting(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	req := &sharedpb.StartDVRRequest{InternalName: "live+s1", TenantId: "tenant-a"}

	mock.ExpectQuery(`SELECT status FROM foghorn\.artifacts`).
		WithArgs("dvr-h", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("starting"))

	st, err := srv.reconcileStartingDVR(context.Background(), req, "dvr-h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st != "starting" {
		t.Fatalf("expected honest in-flight status 'starting', got %q", st)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// Invariant:when a concurrent stop/finalize moved the row terminal, reconciliation must
// surface an error (FailedPrecondition) rather than a false already_started/started.
func TestReconcileStartingDVR_TerminalSurfacesError(t *testing.T) {
	srv, mock := newLifecycleServer(t)
	req := &sharedpb.StartDVRRequest{InternalName: "live+s1", TenantId: "tenant-a"}

	mock.ExpectQuery(`SELECT status FROM foghorn\.artifacts`).
		WithArgs("dvr-h", "tenant-a").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("failed"))

	st, err := srv.reconcileStartingDVR(context.Background(), req, "dvr-h")
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition for a terminal row, got status=%q err=%v", st, err)
	}
	if st != "" {
		t.Fatalf("expected empty status on error, got %q", st)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// An existing active DVR belongs to a different ingest session when its ingest
// generation differs (precise: covers same-node reconnect) or, absent generations,
// its source node differs. Same session/node is a live retry; missing/unparseable
// identity is inconclusive.
func TestDVRRecordingSupersededSession(t *testing.T) {
	dispatch := func(sourceNodeID, ingestGen string) sql.NullString {
		b, err := json.Marshal(jobs.DVRStartDispatch{SourceNodeID: sourceNodeID, IngestGeneration: ingestGen})
		if err != nil {
			t.Fatalf("marshal dispatch: %v", err)
		}
		return sql.NullString{String: string(b), Valid: true}
	}
	cases := []struct {
		name       string
		dispatch   sql.NullString
		liveSource string
		liveGen    string
		want       bool
	}{
		// Precise ingest-generation comparison (both present).
		{"same-node reconnect, different generation is superseded", dispatch("node-a", "gen-old"), "node-a", "gen-new", true},
		{"same session (same generation) is a live retry", dispatch("node-a", "gen-1"), "node-a", "gen-1", false},
		// Node-only fallback when a generation is missing.
		{"another node (no generations) is superseded", dispatch("node-old", ""), "node-new", "", true},
		{"same node (no generations) is a live retry", dispatch("node-a", ""), "node-a", "", false},
		{"blank recorded source, no generations, inconclusive", dispatch("", ""), "node-new", "", false},
		{"blank live source, no generations, inconclusive", dispatch("node-old", ""), "", "", false},
		{"null descriptor is inconclusive", sql.NullString{}, "node-new", "gen-x", false},
		{"unparseable descriptor is inconclusive", sql.NullString{String: "{not json", Valid: true}, "node-new", "gen-x", false},
	}
	for _, tc := range cases {
		if got := dvrRecordingSupersededSession(tc.dispatch, tc.liveSource, tc.liveGen); got != tc.want {
			t.Errorf("%s: dvrRecordingSupersededSession = %v, want %v", tc.name, got, tc.want)
		}
	}
}
