package jobs

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/protobuf/encoding/protojson"
)

// A durable DVR intent whose recording was never created (crash between the synchronous
// intent persist and the async StartDVR) is replayed: StartDVR is invoked bound to the
// session's own generation and dispatched to the session's node.
func TestDVRIntentRecovery_ReplaysStartFromIntent(t *testing.T) {
	intentReq := &sharedpb.StartDVRRequest{TenantId: "t1", InternalName: "live+s1"}
	intentJSON, err := protojson.Marshal(intentReq)
	if err != nil {
		t.Fatalf("marshal intent: %v", err)
	}

	j := NewDVRIntentRecoveryJob(DVRIntentRecoveryConfig{Logger: logging.NewLogger()})
	j.claimIntents = func(_ context.Context, _ time.Duration, _ int) ([]control.UnstartedDVRIntent, error) {
		return []control.UnstartedDVRIntent{{
			SessionID: "gen-1", TenantID: "t1", InternalName: "live+s1", NodeID: "node-1", Intent: intentJSON, Attempts: 1,
		}}, nil
	}
	var gotReq *sharedpb.StartDVRRequest
	var gotNode string
	j.startDVR = func(_ context.Context, req *sharedpb.StartDVRRequest, sourceNodeID string) (*sharedpb.StartDVRResponse, error) {
		gotReq = req
		gotNode = sourceNodeID
		return &sharedpb.StartDVRResponse{DvrHash: "h", Status: "requested"}, nil
	}

	j.reconcile()

	if gotReq == nil {
		t.Fatal("expected StartDVR to be replayed for the claimed intent")
	}
	if gotReq.GetIngestGeneration() != "gen-1" {
		t.Fatalf("replayed start must bind the session generation, got %q", gotReq.GetIngestGeneration())
	}
	if gotReq.GetTenantId() != "t1" || gotReq.GetInternalName() != "live+s1" {
		t.Fatalf("replayed start lost identity: %+v", gotReq)
	}
	if gotNode != "node-1" {
		t.Fatalf("replayed start must dispatch to the session node, got %q", gotNode)
	}
}

// An undecodable payload is moved to the EXPLICIT terminal error state (operator-visible,
// never re-claimed), while a following healthy intent still starts.
func TestDVRIntentRecovery_TerminalErrorForUndecodable(t *testing.T) {
	good := &sharedpb.StartDVRRequest{TenantId: "t2", InternalName: "live+ok"}
	goodJSON, _ := protojson.Marshal(good)

	j := NewDVRIntentRecoveryJob(DVRIntentRecoveryConfig{Logger: logging.NewLogger()})
	j.claimIntents = func(_ context.Context, _ time.Duration, _ int) ([]control.UnstartedDVRIntent, error) {
		return []control.UnstartedDVRIntent{
			{SessionID: "gen-bad", TenantID: "t2", InternalName: "live+bad", NodeID: "n", Intent: []byte("{bad"), Attempts: 1},
			{SessionID: "gen-good", TenantID: "t2", InternalName: "live+ok", NodeID: "n", Intent: goodJSON, Attempts: 1},
		}, nil
	}
	var failed []string
	j.failIntent = func(_ context.Context, _, sessionID, _ string) error { failed = append(failed, sessionID); return nil }
	var started []string
	j.startDVR = func(_ context.Context, req *sharedpb.StartDVRRequest, _ string) (*sharedpb.StartDVRResponse, error) {
		started = append(started, req.GetIngestGeneration())
		return &sharedpb.StartDVRResponse{}, nil
	}

	j.reconcile()

	if len(failed) != 1 || failed[0] != "gen-bad" {
		t.Fatalf("undecodable intent must be terminally failed, got %v", failed)
	}
	if len(started) != 1 || started[0] != "gen-good" {
		t.Fatalf("only the decodable intent should start, got %v", started)
	}
}

// A transient StartDVR failure is NEVER terminalized, no matter how many attempts have
// accumulated — it is always left for the lease-backed retry. Terminalizing a transient failure
// would set dvr_intent_error, which the claim filters out permanently, so a prolonged-but-
// recoverable storage/control outage would silently and PERMANENTLY disable a required recording
// even after the platform recovers. The retry is bounded on the claim side: only intents whose
// session is still active (ended_at IS NULL) are re-claimed, so an ended stream drops out.
func TestDVRIntentRecovery_TransientFailureNeverTerminal(t *testing.T) {
	req := &sharedpb.StartDVRRequest{TenantId: "t3", InternalName: "live+x"}
	reqJSON, _ := protojson.Marshal(req)

	run := func(attempts int) (failedIDs []string) {
		j := NewDVRIntentRecoveryJob(DVRIntentRecoveryConfig{Logger: logging.NewLogger()})
		j.claimIntents = func(_ context.Context, _ time.Duration, _ int) ([]control.UnstartedDVRIntent, error) {
			return []control.UnstartedDVRIntent{{SessionID: "gen-x", TenantID: "t3", InternalName: "live+x", NodeID: "n", Intent: reqJSON, Attempts: attempts}}, nil
		}
		j.failIntent = func(_ context.Context, _, sessionID, _ string) error {
			failedIDs = append(failedIDs, sessionID)
			return nil
		}
		j.startDVR = func(_ context.Context, _ *sharedpb.StartDVRRequest, _ string) (*sharedpb.StartDVRResponse, error) {
			return nil, context.DeadlineExceeded // transient
		}
		j.reconcile()
		return failedIDs
	}

	if got := run(1); len(got) != 0 {
		t.Fatalf("a transient failure must NOT be terminal, got %v", got)
	}
	if got := run(1000); len(got) != 0 {
		t.Fatalf("a transient failure must NOT be terminal even after many attempts, got %v", got)
	}
}
