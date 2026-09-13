package federation

import (
	"database/sql"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
)

// sourceGuardServer builds a Foghorn that would accept an origin-pull for
// "stream" on "source-1", so each test below isolates exactly one refusal.
func sourceGuardServer(t *testing.T) (*FederationServer, *state.StreamStateManager) {
	t.Helper()
	server, _, _ := testFederationServerWithCache(t)
	server.allowFederationMutations = true
	setLiveStreamState(t, "stream", "source-1", "tenant-a", "https://source.example")
	sm := state.DefaultManager()
	t.Cleanup(sm.Shutdown)
	return server, sm
}

func sourceGuardNotification() *foghornfederationpb.OriginPullNotification {
	return &foghornfederationpb.OriginPullNotification{
		StreamName:    "stream",
		TenantId:      "tenant-a",
		SourceNodeId:  "source-1",
		DestClusterId: "cluster-b",
		DestNodeId:    "dest-1",
	}
}

// The fixture must actually be acceptable, or every refusal below proves
// nothing: a test that passes because the setup was broken is worse than none.
func TestPrepareOriginPullAcceptsAServingSource(t *testing.T) {
	server, sm := sourceGuardServer(t)
	sm.UpdateNodeStats("stream", "source-1", 1, 1, 0, 0, false)

	ack, err := server.NotifyOriginPull(svcAuthCtx(), sourceGuardNotification())
	if err != nil {
		t.Fatalf("NotifyOriginPull: %v", err)
	}
	if !ack.GetAccepted() {
		t.Fatalf("baseline source was refused (%q); the refusal tests below would prove nothing", ack.GetReason())
	}
}

// A relayed copy is not a source. Handing back its DTSC URL chains one pull off
// another, so the second cluster's stream depends on a replica that can go away
// without the origin knowing. The guard is unconditional on purpose: gating it
// on a source generation would let a caller omit that field — which a configured
// pull legitimately does — and be handed a replica anyway.
func TestPrepareOriginPullRefusesRelayedSource(t *testing.T) {
	server, sm := sourceGuardServer(t)
	sm.UpdateNodeStats("stream", "source-1", 1, 1, 0, 0, true)

	ack, err := server.NotifyOriginPull(svcAuthCtx(), sourceGuardNotification())
	if err != nil {
		t.Fatalf("NotifyOriginPull: %v", err)
	}
	if ack.GetAccepted() {
		t.Fatal("a relayed instance was offered as a relay source")
	}
	if ack.GetReason() != "source node is a relay" {
		t.Fatalf("reason = %q, want the relay refusal", ack.GetReason())
	}
}

// A node that holds the stream but is neither live nor buffered cannot serve a
// pull; accepting would hand back a DTSC URL that produces nothing.
func TestPrepareOriginPullRefusesIdleSourceNode(t *testing.T) {
	server, sm := sourceGuardServer(t)
	sm.UpdateNodeStats("stream", "source-1", 0, 0, 0, 0, false)
	// Clear the buffer state the fixture set, leaving a node with no inputs.
	if err := sm.UpdateStreamFromBuffer("stream", "stream", "source-1", "tenant-a", "EMPTY", ""); err != nil {
		t.Fatalf("UpdateStreamFromBuffer: %v", err)
	}

	ack, err := server.NotifyOriginPull(svcAuthCtx(), sourceGuardNotification())
	if err != nil {
		t.Fatalf("NotifyOriginPull: %v", err)
	}
	if ack.GetAccepted() {
		t.Fatal("an idle node was offered as a relay source")
	}
	if ack.GetReason() != "source node is not serving this stream" {
		t.Fatalf("reason = %q, want the not-serving refusal", ack.GetReason())
	}
}

// The requester names the tenant it believes owns the stream. If that disagrees
// with local state the pull is refused rather than resolved in the requester's
// favour — this is the boundary that stops one tenant pulling another's media.
func TestPrepareOriginPullRefusesTenantMismatchOnInstance(t *testing.T) {
	server, sm := sourceGuardServer(t)
	sm.UpdateNodeStats("stream", "source-1", 1, 1, 0, 0, false)

	notification := sourceGuardNotification()
	notification.TenantId = "tenant-b"
	ack, err := server.NotifyOriginPull(svcAuthCtx(), notification)
	if err != nil {
		t.Fatalf("NotifyOriginPull: %v", err)
	}
	if ack.GetAccepted() {
		t.Fatal("a foreign tenant was handed this stream's source")
	}
	if ack.GetReason() != "stream tenant mismatch" {
		t.Fatalf("reason = %q, want the tenant refusal", ack.GetReason())
	}
}

// dvr+ takes a different path: it is only Mist-active when a local viewer has
// it open, so presence is proven from the DVR recording row instead. The tenant
// check must survive that detour — it is the same boundary.
func TestPrepareOriginPullRefusesDVRForForeignTenant(t *testing.T) {
	server, _ := sourceGuardServer(t)
	server.db = dvrRecordingDB(t, "abc123", "tenant-b", false)

	notification := sourceGuardNotification()
	notification.StreamName = "dvr+abc123"
	notification.TenantId = "tenant-b"
	ack, err := server.NotifyOriginPull(svcAuthCtx(), notification)
	if err != nil {
		t.Fatalf("NotifyOriginPull: %v", err)
	}
	if ack.GetAccepted() {
		t.Fatal("a foreign tenant was handed a DVR source")
	}
	if ack.GetReason() != "dvr not recording locally" {
		t.Fatalf("reason = %q, want the tenant refusal", ack.GetReason())
	}
}

func TestPrepareOriginPullAcceptsDVRWithoutLivePublisher(t *testing.T) {
	server, _ := sourceGuardServer(t)
	server.db = dvrRecordingDB(t, "abc123", "tenant-a", true)
	notification := sourceGuardNotification()
	notification.StreamName = "dvr+abc123"
	ack, err := server.NotifyOriginPull(svcAuthCtx(), notification)
	if err != nil || !ack.GetAccepted() || control.SourcePullCredential(ack.GetDtscUrl()) == "" {
		t.Fatalf("active DVR source not admitted: accepted=%v reason=%q error=%v", ack.GetAccepted(), ack.GetReason(), err)
	}
	if ack.GetSourceGeneration() != "" || ack.GetSourceRevision() != 0 {
		t.Fatal("DVR source invented a live publisher generation")
	}
}

// A dvr+ name that is not recording here has nothing to pull, whatever Mist
// state says about the live stream behind it.
func TestPrepareOriginPullRefusesDVRThatIsNotRecording(t *testing.T) {
	server, _ := sourceGuardServer(t)
	server.db = dvrRecordingDB(t, "abc123", "tenant-a", false)

	notification := sourceGuardNotification()
	notification.StreamName = "dvr+abc123"
	ack, err := server.NotifyOriginPull(svcAuthCtx(), notification)
	if err != nil {
		t.Fatalf("NotifyOriginPull: %v", err)
	}
	if ack.GetAccepted() {
		t.Fatal("a DVR that is not recording locally was offered as a source")
	}
	if ack.GetReason() != "dvr not recording locally" {
		t.Fatalf("reason = %q, want the not-recording refusal", ack.GetReason())
	}
}

func dvrRecordingDB(t *testing.T, token, tenantID string, recording bool) *sql.DB {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		db.Close()
	})
	mock.ExpectQuery(regexp.QuoteMeta("SELECT EXISTS (")).WithArgs(tenantID, token, "source-1").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(recording))
	return db
}

func TestPrepareOriginPullRefusesUnknownStream(t *testing.T) {
	server, _ := sourceGuardServer(t)
	notification := sourceGuardNotification()
	notification.StreamName = "not-here"
	notification.SourceNodeId = "source-1"

	ack, err := server.NotifyOriginPull(svcAuthCtx(), notification)
	if err != nil {
		t.Fatalf("NotifyOriginPull: %v", err)
	}
	if ack.GetAccepted() {
		t.Fatal("a stream absent from local state was offered as a source")
	}
	if ack.GetReason() != "stream not found locally" {
		t.Fatalf("reason = %q, want the not-found refusal", ack.GetReason())
	}
}
