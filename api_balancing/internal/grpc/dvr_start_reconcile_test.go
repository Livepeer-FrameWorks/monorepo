package grpc

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_balancing/internal/control"
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
