package control

import (
	"context"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ControlFeaturesForProtocol is the single source of a sidecar session's protocol-gated capabilities. Locking the
// thresholds here keeps the extension seam explicit: staged freeze at v1, staged thumbnail + authoritative inventory
// at v2. A version below a floor yields false — but registration rejects any sidecar below MinControlProtocolVersion,
// so no admitted session is ever below the floors (see the invariant test below).
func TestControlFeaturesForProtocol_Thresholds(t *testing.T) {
	cases := []struct {
		v                                                                   int32
		freeze, thumbnail, authInventory, restreamRevision, restreamAttempt bool
	}{
		{-1, false, false, false, false, false}, // never negative in practice, but must not misclassify
		{0, false, false, false, false, false},  // pre-staged sidecar (Register.control_protocol_version absent)
		{1, true, false, false, false, false},   // staged freeze only
		{2, true, true, true, false, false},     // + staged thumbnail + versioned inventory
		{4, true, true, true, false, false},     // rolling-upgrade legacy restream acknowledgements
		{5, true, true, true, true, false},      // exact restream desired-set revision fencing
		{6, true, true, true, true, true},       // + runtime-attempt fencing
		{7, true, true, true, true, true},       // future versions keep every earlier capability
	}
	for _, c := range cases {
		got := ControlFeaturesForProtocol(c.v)
		if got.StagedFreeze != c.freeze || got.StagedThumbnail != c.thumbnail || got.AuthoritativeInventory != c.authInventory || got.RestreamRevisionFence != c.restreamRevision || got.RestreamAttemptFence != c.restreamAttempt {
			t.Fatalf("v=%d: got %+v, want freeze=%v thumbnail=%v authInventory=%v restreamRevision=%v restreamAttempt=%v",
				c.v, got, c.freeze, c.thumbnail, c.authInventory, c.restreamRevision, c.restreamAttempt)
		}
	}
}

// Registration rejects anything below MinControlProtocolVersion, so every ADMITTED session is inventory-authoritative
// (and staged-capable). This locks that invariant: if the minimum is ever lowered below the authoritative-inventory
// floor, an admitted session could send unversioned inventory with no handler for it, so this fails loudly to signal
// that a supported protocol RANGE would first be required.
func TestMinControlProtocolVersion_AdmittedSessionsAreAuthoritative(t *testing.T) {
	if MinControlProtocolVersion < AuthoritativeInventoryProtocolMin {
		t.Fatalf("MinControlProtocolVersion (%d) below AuthoritativeInventoryProtocolMin (%d): an admitted session could send unversioned inventory with no legacy path",
			MinControlProtocolVersion, AuthoritativeInventoryProtocolMin)
	}
	if f := ControlFeaturesForProtocol(MinControlProtocolVersion); !f.AuthoritativeInventory || !f.StagedFreeze || !f.StagedThumbnail {
		t.Fatalf("a session at the minimum protocol must hold every capability, got %+v", f)
	}
	if MinControlProtocolVersion < IngestGenerationFencingProtocolMin {
		t.Fatalf("minimum protocol %d admits a sidecar without ingest generation fencing (requires %d)", MinControlProtocolVersion, IngestGenerationFencingProtocolMin)
	}
}

func TestLegacyRestreamTeardownCompletionBoundary(t *testing.T) {
	if !legacyRestreamTeardownCompletesOnDispatch(4) {
		t.Fatal("protocol 4 has no correlated deactivation acknowledgement and must settle on successful dispatch")
	}
	if legacyRestreamTeardownCompletesOnDispatch(5) {
		t.Fatal("protocol 5 must wait for its correlated deactivation acknowledgement")
	}
}

func TestProtocolFourRestreamTeardownSettlesAfterSuccessfulDispatch(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	previousDB := db
	SetDB(database)
	t.Cleanup(func() { SetDB(previousDB) })
	previousRegistry := registry
	t.Cleanup(func() { registry = previousRegistry })

	stream := &mockStream{}
	registry = &Registry{conns: map[string]*conn{
		"node-v4": {stream: stream, rawNodeID: "node-v4", canonicalID: "node-v4", protocolVersion: 4},
	}, log: logging.NewLogger()}
	const generation = "11111111-1111-4111-8111-111111111111"
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.ingest_offline_effects")).
		WithArgs("node-v4", generation).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.ingest_offline_effects")).
		WithArgs("node-v4", generation).
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := SendLocalDeactivatePushTargets(context.Background(), "node-v4", &ipcpb.DeactivatePushTargets{
		StreamName: "live+legacy", SourceGeneration: generation, TargetRevision: 7,
	}); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetDeactivatePushTargets() == nil {
		t.Fatalf("protocol-4 teardown was not dispatched: %+v", stream.sent)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRestreamActivationDispatchRequiresProtocolSixAndAttempt(t *testing.T) {
	previousRegistry := registry
	t.Cleanup(func() { registry = previousRegistry })
	stream := &mockStream{}
	connection := &conn{stream: stream, rawNodeID: "node", canonicalID: "node", protocolVersion: RestreamRevisionFenceProtocolMin}
	registry = &Registry{conns: map[string]*conn{"node": connection}, log: logging.NewLogger()}
	request := &ipcpb.ActivatePushTargets{
		StreamName: "live+attempt", SourceGeneration: "generation", TargetRevision: 1, ActivationAttempt: "attempt",
	}
	if err := SendLocalActivatePushTargets(context.Background(), "node", request); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("protocol-5 activation error=%v, want FailedPrecondition", err)
	}
	connection.protocolVersion = RestreamAttemptFenceProtocolMin
	request.ActivationAttempt = ""
	if err := SendLocalActivatePushTargets(context.Background(), "node", request); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("attempt-less activation error=%v, want FailedPrecondition", err)
	}
	request.ActivationAttempt = "attempt"
	if err := SendLocalActivatePushTargets(context.Background(), "node", request); err != nil {
		t.Fatal(err)
	}
	if len(stream.sent) != 1 || stream.sent[0].GetActivatePushTargets().GetActivationAttempt() != "attempt" {
		t.Fatalf("protocol-6 activation was not dispatched with its attempt: %+v", stream.sent)
	}
}
