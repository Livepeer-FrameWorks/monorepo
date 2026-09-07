package triggers

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/pushstatusoutbox"
	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	fieldcrypto "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestUnsupportedRestreamProtocolReleasesCapacityAndUsesUpgradeReason(t *testing.T) {
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previousDB := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(previousDB) })

	const (
		tenantID   = "11111111-1111-4111-8111-111111111111"
		targetID   = "22222222-2222-4222-8222-222222222222"
		generation = "33333333-3333-4333-8333-333333333333"
	)
	effect := control.AdmissionEffect{
		TenantID: tenantID, InternalName: "protocol-upgrade", NodeID: "node-a", SourceGeneration: generation,
		DrainDone: true, BroadcastDone: true, DecklogDone: true,
	}
	activation := &ipcpb.ActivatePushTargets{
		StreamName: "live+protocol-upgrade", TargetRevision: 7, MaxViewers: 1,
		Targets: []*ipcpb.PushTargetSpec{{TargetId: targetID, TargetUri: "rtmp://example.test/live/key"}},
	}
	untrackPushTargets(activation.GetStreamName())
	t.Cleanup(func() { untrackPushTargets(activation.GetStreamName()) })
	trackPushTargetsForGeneration(activation.GetStreamName(), tenantID, "", generation, 7, "node-a", 1,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: activation.GetTargets()[0].GetTargetUri()}})
	if _, reserveErr := newTestProcessor(t).reserveRestreamCapacity(context.Background(), effect, activation, 1); reserveErr != nil {
		t.Fatal(reserveErr)
	}
	if got := capacity.CountViewers(tenantID); got != 1 {
		t.Fatalf("reserved viewer-like capacity=%d, want 1", got)
	}
	effect.PushTargets, err = proto.Marshal(activation)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.push_target_status_outbox")).
		WithArgs(targetID, tenantID, "failed", pushstatusoutbox.ReasonEdgeUpgradeRequired, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	p := newTestProcessor(t)
	p.nodeOwnedLocally = func(string) bool { return true }
	p.restreamAttemptSupport = func(string) error {
		return status.Error(codes.FailedPrecondition, "protocol too old")
	}
	legs, applyErr := p.ApplyAdmissionEffect(context.Background(), effect)
	if applyErr != nil {
		t.Fatalf("ApplyAdmissionEffect: %v", applyErr)
	}
	if !legs.ActivationDone || legs.CapacityPending == nil || *legs.CapacityPending {
		t.Fatalf("unsupported protocol legs=%+v, want settled with no pending capacity", legs)
	}
	if got := capacity.CountViewers(tenantID); got != 0 {
		t.Fatalf("unsupported protocol retained viewer-like capacity=%d", got)
	}
	if tracked, found := lookupPushTargetID(activation.GetStreamName(), targetID, generation, 7); !found || tracked.Current {
		t.Fatalf("unsupported protocol left target current: found=%v tracked=%+v", found, tracked)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFinalRestreamStatusBindsPreviouslyUnobservedMistPushID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previousDB := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(previousDB) })

	const (
		streamName = "live+bind-from-final"
		tenantID   = "11111111-1111-4111-8111-111111111111"
		streamID   = "22222222-2222-4222-8222-222222222222"
		targetID   = "33333333-3333-4333-8333-333333333333"
		generation = "44444444-4444-4444-8444-444444444444"
		attempt    = "55555555-5555-4555-8555-555555555555"
	)
	untrackPushTargets(streamName)
	t.Cleanup(func() { untrackPushTargets(streamName) })
	trackPushTargetsForGeneration(streamName, tenantID, streamID, generation, 7, "node-a", 0,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: "rtmp://example.test/live/key"}}, attempt)

	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.admission_push_target_revisions")).
		WithArgs(targetID, int64(91), "node-a", generation, int64(7), attempt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.push_target_status_outbox")).
		WithArgs(targetID, tenantID, "idle", pushstatusoutbox.ReasonStopped, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	capture, client := startDecklogCapture(t)
	p := newTestProcessor(t)
	p.decklogClient = client
	_, _, err = p.handleRestreamStatus(&ipcpb.MistTrigger{
		NodeId: "node-a", TriggerType: "RESTREAM_STATUS_FINAL", TenantId: proto.String(tenantID),
		TriggerPayload: &ipcpb.MistTrigger_RestreamStatus{RestreamStatus: &ipcpb.PushTargetStatusReport{
			TargetId: targetID, TenantId: tenantID, StreamId: streamID, StreamName: streamName,
			SourceGeneration: generation, TargetRevision: 7, ActivationAttempt: attempt, MistPushId: 91,
			State: ipcpb.RestreamState_RESTREAM_STATE_IDLE, Reason: ipcpb.RestreamReason_RESTREAM_REASON_STOPPED,
			SourceEventId: "final-event", StartedAtMs: time.Now().Add(-time.Minute).UnixMilli(), EndedAtMs: time.Now().UnixMilli(),
		}},
	})
	if err != nil {
		t.Fatalf("bind final: %v", err)
	}
	tracked, found := lookupPushTargetID(streamName, targetID, generation, 7)
	if !found || tracked.Current || tracked.MistPushID != 91 {
		t.Fatalf("terminal final did not retire the bound identity: found=%v tracked=%+v", found, tracked)
	}
	if got := capture.received(); len(got) != 1 || got[0].GetRestreamStatus().GetMistPushId() != 91 {
		t.Fatalf("final was not forwarded after bind: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFinalRestreamStatusAcceptsUnboundLegacyMistPushID(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previousDB := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(previousDB) })

	const (
		streamName = "live+accept-unbound-final"
		tenantID   = "51111111-1111-4111-8111-111111111111"
		streamID   = "52222222-2222-4222-8222-222222222222"
		targetID   = "53333333-3333-4333-8333-333333333333"
		generation = "54444444-4444-4444-8444-444444444444"
	)
	untrackPushTargets(streamName)
	t.Cleanup(func() { untrackPushTargets(streamName) })
	trackPushTargetsForGeneration(streamName, tenantID, streamID, generation, 7, "node-a", 0,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: "rtmp://example.test/live/key"}})

	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.push_target_status_outbox")).
		WithArgs(targetID, tenantID, "idle", pushstatusoutbox.ReasonStopped, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	capture, client := startDecklogCapture(t)
	p := newTestProcessor(t)
	p.decklogClient = client
	_, _, err = p.handleRestreamStatus(&ipcpb.MistTrigger{
		NodeId: "node-a", TriggerType: "RESTREAM_STATUS_FINAL", TenantId: proto.String(tenantID),
		TriggerPayload: &ipcpb.MistTrigger_RestreamStatus{RestreamStatus: &ipcpb.PushTargetStatusReport{
			TargetId: targetID, TenantId: tenantID, StreamId: streamID, StreamName: streamName,
			SourceGeneration: generation, TargetRevision: 7,
			State: ipcpb.RestreamState_RESTREAM_STATE_IDLE, Reason: ipcpb.RestreamReason_RESTREAM_REASON_STOPPED,
			SourceEventId: "legacy-final", StartedAtMs: time.Now().Add(-time.Minute).UnixMilli(), EndedAtMs: time.Now().UnixMilli(),
		}},
	})
	if err != nil {
		t.Fatalf("accept unbound final: %v", err)
	}
	if got := capture.received(); len(got) != 1 || got[0].GetRestreamStatus().GetSourceEventId() != "legacy-final" {
		t.Fatalf("unbound final was not forwarded: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReserveRestreamCapacityAdmitsDeterministicSubsetFromViewerPool(t *testing.T) {
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })
	p := newTestProcessor(t)
	effect := control.AdmissionEffect{TenantID: "tenant-restream", NodeID: "node-a", SourceGeneration: "generation-a"}
	activation := &ipcpb.ActivatePushTargets{
		StreamName: "live+demo",
		Targets: []*ipcpb.PushTargetSpec{
			{TargetId: "target-a"},
			{TargetId: "target-b"},
		},
	}

	pending, err := p.reserveRestreamCapacity(context.Background(), effect, activation, 1)
	if err != nil {
		t.Fatalf("capacity denial for one target must not reject the admitted target: %v", err)
	}
	if !pending {
		t.Fatal("denied target must leave a durable capacity-pending obligation")
	}
	if got := capacity.CountViewers(effect.TenantID); got != 1 {
		t.Fatalf("one deterministic restream target should consume the shared slot: count=%d", got)
	}
	if len(activation.GetTargets()) != 1 || activation.GetTargets()[0].GetTargetId() != "target-a" {
		t.Fatalf("admission must select the sorted first target: %+v", activation.GetTargets())
	}
}

func TestRestreamRevisionReplacementDoesNotDoubleReserveViewerCapacity(t *testing.T) {
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })
	p := newTestProcessor(t)
	effect := control.AdmissionEffect{TenantID: "tenant-restream", NodeID: "node-a", SourceGeneration: "generation-a"}
	for _, revision := range []int64{1, 2} {
		activation := &ipcpb.ActivatePushTargets{
			StreamName: "live+demo", SourceGeneration: effect.SourceGeneration, TargetRevision: revision,
			Targets: []*ipcpb.PushTargetSpec{{TargetId: "target-a"}},
		}
		if pending, err := p.reserveRestreamCapacity(context.Background(), effect, activation, 1); err != nil || pending {
			t.Fatalf("revision %d reserve: pending=%v err=%v", revision, pending, err)
		}
	}
	if got := capacity.CountViewers(effect.TenantID); got != 1 {
		t.Fatalf("one target across revisions must consume exactly one viewer-like slot: count=%d", got)
	}
}

func TestDeactivationAckReleasesCapacityWhenTerminalStatusWasLost(t *testing.T) {
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previousDB := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(previousDB) })

	const (
		streamName = "live+lost-terminal"
		tenantID   = "11111111-1111-4111-8111-111111111111"
		streamID   = "22222222-2222-4222-8222-222222222222"
		targetID   = "33333333-3333-4333-8333-333333333333"
		generation = "44444444-4444-4444-8444-444444444444"
	)
	p := newTestProcessor(t)
	effect := control.AdmissionEffect{TenantID: tenantID, NodeID: "node-a", SourceGeneration: generation}
	activation := &ipcpb.ActivatePushTargets{
		StreamName: streamName, SourceGeneration: generation, TargetRevision: 9,
		Targets: []*ipcpb.PushTargetSpec{{TargetId: targetID}},
	}
	if _, err := p.reserveRestreamCapacity(context.Background(), effect, activation, 1); err != nil {
		t.Fatal(err)
	}
	trackPushTargetsForGeneration(streamName, tenantID, streamID, generation, 9, "node-a", 1,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: "rtmp://example.test/live/key"}})
	t.Cleanup(func() { untrackPushTargets(streamName) })

	mock.ExpectQuery(`FROM foghorn\.admission_push_target_revisions AS history`).
		WithArgs(generation, "node-a", int64(9), "").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "stream_internal_name", "node_id", "push_targets", "target_revision"}))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.push_target_status_outbox")).
		WithArgs(targetID, tenantID, "idle", pushstatusoutbox.ReasonStopped, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	if err := p.HandlePushTargetDeactivation("node-a", &ipcpb.DeactivatePushTargetsResult{
		StreamName: streamName, SourceGeneration: generation, TargetRevision: 9, Converged: true,
	}); err != nil {
		t.Fatalf("HandlePushTargetDeactivation: %v", err)
	}
	if got := capacity.CountViewers(tenantID); got != 0 {
		t.Fatalf("deactivation acknowledgement retained viewer capacity after lost status: %d", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCapacityRenewalEvictsEndedGenerationWithoutTerminalMessages(t *testing.T) {
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previousDB := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(previousDB) })

	const (
		streamName = "live+renewal-ended"
		tenantID   = "81111111-1111-4111-8111-111111111111"
		streamID   = "82222222-2222-4222-8222-222222222222"
		targetID   = "83333333-3333-4333-8333-333333333333"
		generation = "84444444-4444-4444-8444-444444444444"
	)
	p := newTestProcessor(t)
	effect := control.AdmissionEffect{TenantID: tenantID, NodeID: "node-a", SourceGeneration: generation}
	activation := &ipcpb.ActivatePushTargets{
		StreamName: streamName, SourceGeneration: generation, TargetRevision: 3,
		Targets: []*ipcpb.PushTargetSpec{{TargetId: targetID}},
	}
	if _, err := p.reserveRestreamCapacity(context.Background(), effect, activation, 1); err != nil {
		t.Fatal(err)
	}
	trackPushTargetsForGeneration(streamName, tenantID, streamID, generation, 3, "node-a", 1,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: "rtmp://example.test/live/key"}})
	t.Cleanup(func() { untrackPushTargets(streamName) })

	mock.ExpectBegin()
	mock.ExpectQuery(`pg_try_advisory_xact_lock`).WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(false))
	mock.ExpectRollback()
	mock.ExpectBegin()
	mock.ExpectQuery(`pg_try_advisory_xact_lock`).WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(false))
	mock.ExpectRollback()
	mock.ExpectQuery(`SELECT EXISTS .*foghorn\.ingest_sessions`).
		WithArgs(tenantID, "renewal-ended", generation).
		WillReturnRows(sqlmock.NewRows([]string{"is_current", "source_revision", "projection_state"}).AddRow(false, nil, "finalized"))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.push_target_status_outbox")).
		WithArgs(targetID, tenantID, "idle", pushstatusoutbox.ReasonStopped, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	p.renewRestreamCapacityOnce(context.Background())
	if got := capacity.CountViewers(tenantID); got != 0 {
		t.Fatalf("ended generation was renewed after terminal messages were lost: count=%d", got)
	}
	if current, found := lookupPushTargetID(streamName, targetID, generation, 3); !found || current.Current {
		t.Fatalf("ended generation remained current: found=%v info=%+v", found, current)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCapacityRenewalKeepsLiveGenerationReserved(t *testing.T) {
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previousDB := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(previousDB) })

	const (
		streamName = "live+renewal-live"
		tenantID   = "91111111-1111-4111-8111-111111111111"
		streamID   = "92222222-2222-4222-8222-222222222222"
		targetID   = "93333333-3333-4333-8333-333333333333"
		generation = "94444444-4444-4444-8444-444444444444"
	)
	p := newTestProcessor(t)
	effect := control.AdmissionEffect{TenantID: tenantID, NodeID: "node-a", SourceGeneration: generation}
	activation := &ipcpb.ActivatePushTargets{
		StreamName: streamName, SourceGeneration: generation, TargetRevision: 4,
		Targets: []*ipcpb.PushTargetSpec{{TargetId: targetID}},
	}
	if _, err := p.reserveRestreamCapacity(context.Background(), effect, activation, 1); err != nil {
		t.Fatal(err)
	}
	trackPushTargetsForGeneration(streamName, tenantID, streamID, generation, 4, "node-a", 1,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: "rtmp://example.test/live/key"}})
	t.Cleanup(func() { untrackPushTargets(streamName) })

	mock.ExpectBegin()
	mock.ExpectQuery(`pg_try_advisory_xact_lock`).WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(false))
	mock.ExpectRollback()
	mock.ExpectBegin()
	mock.ExpectQuery(`pg_try_advisory_xact_lock`).WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(false))
	mock.ExpectRollback()
	mock.ExpectQuery(`SELECT EXISTS .*foghorn\.ingest_sessions`).
		WithArgs(tenantID, "renewal-live", generation).
		WillReturnRows(sqlmock.NewRows([]string{"is_current", "source_revision", "projection_state"}).AddRow(true, int64(9), "active"))

	p.renewRestreamCapacityOnce(context.Background())
	if got := capacity.CountViewers(tenantID); got != 1 {
		t.Fatalf("live generation lost its viewer-like reservation: count=%d", got)
	}
	if current, found := lookupPushTargetID(streamName, targetID, generation, 4); !found || !current.Current {
		t.Fatalf("live generation was retired: found=%v info=%+v", found, current)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalActivationPersistsFailureAndReleasesCapacityBeforeSettlement(t *testing.T) {
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previousDB := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(previousDB) })

	const (
		streamName = "live+terminal"
		tenantID   = "33333333-3333-4333-8333-333333333333"
		targetID   = "44444444-4444-4444-8444-444444444444"
		generation = "55555555-5555-4555-8555-555555555555"
	)
	effect := control.AdmissionEffect{TenantID: tenantID, NodeID: "node-a", SourceGeneration: generation}
	activation := &ipcpb.ActivatePushTargets{
		StreamName: streamName, SourceGeneration: generation, TargetRevision: 7, MaxViewers: 1,
		Targets: []*ipcpb.PushTargetSpec{{TargetId: targetID, TargetUri: "rtmp://127.0.0.1/live/key"}},
	}
	p := newTestProcessor(t)
	if _, reserveErr := p.reserveRestreamCapacity(context.Background(), effect, activation, 1); reserveErr != nil {
		t.Fatal(reserveErr)
	}
	trackPushTargetsForGeneration(streamName, tenantID, "66666666-6666-4666-8666-666666666666", generation, 7, "node-a", 1,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: activation.Targets[0].TargetUri}})
	// Helmsman emits the terminal status before its correlated activation result.
	// The status path retires tracking, but the result still has to settle the
	// durable activation obligation idempotently.
	retirePushTargetID(streamName, targetID, generation, 7)
	t.Cleanup(func() { untrackPushTargets(streamName) })
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.push_target_status_outbox")).
		WithArgs(targetID, tenantID, "failed", pushstatusoutbox.ReasonDestinationRejected, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	err = p.HandleTerminalPushTargetActivation("node-a", &ipcpb.ActivatePushTargetsResult{
		StreamName: streamName, SourceGeneration: generation, TargetRevision: 7,
		Targets: []*ipcpb.PushTargetConvergence{{TargetId: targetID, Reason: ipcpb.RestreamReason_RESTREAM_REASON_DESTINATION_REJECTED}},
	})
	if err != nil {
		t.Fatalf("HandleTerminalPushTargetActivation: %v", err)
	}
	if got := capacity.CountViewers(tenantID); got != 0 {
		t.Fatalf("terminal target retained viewer capacity: %d", got)
	}
	tracked, found := lookupPushTargetID(streamName, targetID, generation, 7)
	if !found || tracked.Current {
		t.Fatalf("terminal target tracking was not retired: found=%v tracked=%+v", found, tracked)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalActivationRejectsShortOutcomeSet(t *testing.T) {
	const (
		streamName = "live+terminal-short"
		tenantID   = "33333333-3333-4333-8333-333333333333"
		generation = "55555555-5555-4555-8555-555555555555"
	)
	trackPushTargetsForGeneration(streamName, tenantID, "66666666-6666-4666-8666-666666666666", generation, 7, "node-a", 2,
		[]*commodorepb.PushTargetInternal{
			{Id: "44444444-4444-4444-8444-444444444441", TargetUri: "rtmp://127.0.0.1/live/a"},
			{Id: "44444444-4444-4444-8444-444444444442", TargetUri: "rtmp://127.0.0.1/live/b"},
		})
	t.Cleanup(func() { untrackPushTargets(streamName) })
	p := newTestProcessor(t)
	err := p.HandleTerminalPushTargetActivation("node-a", &ipcpb.ActivatePushTargetsResult{
		StreamName: streamName, SourceGeneration: generation, TargetRevision: 7,
		Targets: []*ipcpb.PushTargetConvergence{{
			TargetId: "44444444-4444-4444-8444-444444444441",
			Reason:   ipcpb.RestreamReason_RESTREAM_REASON_DESTINATION_REJECTED,
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "omits dispatched target") {
		t.Fatalf("short terminal result error = %v, want omitted-target rejection", err)
	}
}

func TestTrackingSuccessorGenerationRetainsLateFinalIdentity(t *testing.T) {
	const (
		streamName = "live+generation-fence"
		targetID   = "77777777-7777-4777-8777-777777777777"
		oldGen     = "88888888-8888-4888-8888-888888888888"
		newGen     = "99999999-9999-4999-8999-999999999999"
	)
	untrackPushTargets(streamName)
	t.Cleanup(func() { untrackPushTargets(streamName) })
	trackPushTargetsForGeneration(streamName, "tenant-a", "stream-a", oldGen, 3, "node-a", 1,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: "rtmp://example.com/old"}})
	trackPushTargetsForGeneration(streamName, "tenant-a", "stream-a", newGen, 4, "node-a", 1,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: "rtmp://example.com/new"}})

	old, found := lookupPushTargetID(streamName, targetID, oldGen, 3)
	if !found || old.Current || old.SourceGeneration != oldGen {
		t.Fatalf("late final identity was discarded by successor generation: found=%v info=%+v", found, old)
	}
	current, found := lookupPushTargetID(streamName, targetID, newGen, 4)
	if !found || !current.Current {
		t.Fatalf("successor generation is not current: found=%v info=%+v", found, current)
	}
}

func TestRetiredLatestRestreamStatusPersistsIdle(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previousDB := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(previousDB) })

	const (
		streamName = "live+teardown-status"
		tenantID   = "11111111-1111-4111-8111-111111111111"
		streamID   = "22222222-2222-4222-8222-222222222222"
		targetID   = "33333333-3333-4333-8333-333333333333"
		generation = "44444444-4444-4444-8444-444444444444"
	)
	untrackPushTargets(streamName)
	t.Cleanup(func() { untrackPushTargets(streamName) })
	trackPushTargetsForGeneration(streamName, tenantID, streamID, generation, 9, "node-a", 0,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: "rtmp://example.test/live/key"}})
	retirePushTargetID(streamName, targetID, generation, 9)

	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.push_target_status_outbox")).
		WithArgs(targetID, tenantID, "idle", pushstatusoutbox.ReasonStopped, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	p := newTestProcessor(t)
	_, _, err = p.handleRestreamStatus(&ipcpb.MistTrigger{
		NodeId: "node-a",
		TriggerPayload: &ipcpb.MistTrigger_RestreamStatus{RestreamStatus: &ipcpb.PushTargetStatusReport{
			TargetId: targetID, TenantId: tenantID, StreamId: streamID, StreamName: streamName,
			SourceGeneration: generation, TargetRevision: 9,
			State: ipcpb.RestreamState_RESTREAM_STATE_IDLE, Reason: ipcpb.RestreamReason_RESTREAM_REASON_STOPPED,
		}},
	})
	if err != nil {
		t.Fatalf("handle retired teardown status: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestFinalRestreamStatusRejectsNonTerminalState(t *testing.T) {
	const (
		streamName = "live+nonterminal-final"
		tenantID   = "21111111-1111-4111-8111-111111111111"
		streamID   = "22222222-2222-4222-8222-222222222222"
		targetID   = "23333333-3333-4333-8333-333333333333"
		generation = "24444444-4444-4444-8444-444444444444"
	)
	untrackPushTargets(streamName)
	t.Cleanup(func() { untrackPushTargets(streamName) })
	trackPushTargetsForGeneration(streamName, tenantID, streamID, generation, 1, "node-a", 0,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: "rtmp://example.test/live/key"}})

	p := newTestProcessor(t)
	_, _, err := p.handleRestreamStatus(&ipcpb.MistTrigger{
		NodeId: "node-a", TriggerType: "RESTREAM_STATUS_FINAL",
		TriggerPayload: &ipcpb.MistTrigger_RestreamStatus{RestreamStatus: &ipcpb.PushTargetStatusReport{
			TargetId: targetID, TenantId: tenantID, StreamId: streamID, StreamName: streamName,
			SourceGeneration: generation, TargetRevision: 1,
			State: ipcpb.RestreamState_RESTREAM_STATE_PUSHING,
		}},
	})
	if err == nil {
		t.Fatal("nonterminal final restream status was accepted")
	}
}

func TestReplayedFinalCannotReleaseReplacementMistPush(t *testing.T) {
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })
	const (
		streamName = "live+replayed-final"
		tenantID   = "31111111-1111-4111-8111-111111111111"
		streamID   = "32222222-2222-4222-8222-222222222222"
		targetID   = "33333333-3333-4333-8333-333333333333"
		generation = "34444444-4444-4444-8444-444444444444"
	)
	untrackPushTargets(streamName)
	t.Cleanup(func() { untrackPushTargets(streamName) })
	p := newTestProcessor(t)
	activation := &ipcpb.ActivatePushTargets{
		StreamName: streamName, TenantId: tenantID, StreamId: streamID,
		SourceGeneration: generation, TargetRevision: 5, MaxViewers: 1,
		Targets: []*ipcpb.PushTargetSpec{{TargetId: targetID}},
	}
	if pending, err := p.reserveRestreamCapacity(context.Background(), control.AdmissionEffect{
		TenantID: tenantID, NodeID: "node-a", SourceGeneration: generation,
	}, activation, 1); err != nil || pending {
		t.Fatalf("reserve replacement: pending=%v err=%v", pending, err)
	}
	trackPushTargetsForGeneration(streamName, tenantID, streamID, generation, 5, "node-a", 1,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: "rtmp://example.test/live/key"}})
	if err := p.HandlePushTargetActivationResult("node-a", &ipcpb.ActivatePushTargetsResult{
		StreamName: streamName, SourceGeneration: generation, TargetRevision: 5,
		Targets: []*ipcpb.PushTargetConvergence{{TargetId: targetID, MistPushId: 22, Active: true}},
	}); err != nil {
		t.Fatal(err)
	}

	capture, client := startDecklogCapture(t)
	p.decklogClient = client
	_, _, err := p.handleRestreamStatus(&ipcpb.MistTrigger{
		NodeId: "node-a", TriggerType: "RESTREAM_STATUS_FINAL",
		TriggerPayload: &ipcpb.MistTrigger_RestreamStatus{RestreamStatus: &ipcpb.PushTargetStatusReport{
			TargetId: targetID, TenantId: tenantID, StreamId: streamID, StreamName: streamName,
			SourceGeneration: generation, TargetRevision: 5, MistPushId: 21,
			State: ipcpb.RestreamState_RESTREAM_STATE_IDLE, Reason: ipcpb.RestreamReason_RESTREAM_REASON_COMPLETED,
		}},
	})
	if err != nil {
		t.Fatalf("replayed final: %v", err)
	}
	if got := capacity.CountViewers(tenantID); got != 1 {
		t.Fatalf("replayed final released replacement capacity: %d", got)
	}
	tracked, found := lookupPushTargetID(streamName, targetID, generation, 5)
	if !found || !tracked.Current || tracked.MistPushID != 22 {
		t.Fatalf("replacement identity was retired: found=%v tracked=%+v", found, tracked)
	}
	if got := capture.received(); len(got) != 1 || got[0].GetRestreamStatus().GetMistPushId() != 21 {
		t.Fatalf("replayed final fact was not published: %+v", got)
	}
}

func TestReplayedFinalFromPriorAttemptCannotRetireReplacement(t *testing.T) {
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })
	const (
		streamName = "live+attempt-replay"
		tenantID   = "71111111-1111-4111-8111-111111111111"
		streamID   = "72222222-2222-4222-8222-222222222222"
		targetID   = "73333333-3333-4333-8333-333333333333"
		generation = "74444444-4444-4444-8444-444444444444"
	)
	untrackPushTargets(streamName)
	t.Cleanup(func() { untrackPushTargets(streamName) })
	p := newTestProcessor(t)
	activation := &ipcpb.ActivatePushTargets{
		StreamName: streamName, TenantId: tenantID, StreamId: streamID,
		SourceGeneration: generation, TargetRevision: 5, MaxViewers: 1,
		Targets: []*ipcpb.PushTargetSpec{{TargetId: targetID}},
	}
	if pending, err := p.reserveRestreamCapacity(context.Background(), control.AdmissionEffect{
		TenantID: tenantID, NodeID: "node-a", SourceGeneration: generation,
	}, activation, 1); err != nil || pending {
		t.Fatalf("reserve replacement: pending=%v err=%v", pending, err)
	}
	targets := []*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: "rtmp://example.test/live/key"}}
	trackPushTargetsForGeneration(streamName, tenantID, streamID, generation, 5, "node-a", 1, targets, "attempt-old")
	if err := p.HandlePushTargetActivationResult("node-a", &ipcpb.ActivatePushTargetsResult{
		StreamName: streamName, SourceGeneration: generation, TargetRevision: 5, ActivationAttempt: "attempt-old",
		Targets: []*ipcpb.PushTargetConvergence{{TargetId: targetID, MistPushId: 21, Active: true}},
	}); err != nil {
		t.Fatal(err)
	}
	trackPushTargetsForGeneration(streamName, tenantID, streamID, generation, 5, "node-a", 1, targets, "attempt-new")
	if err := p.HandlePushTargetActivationResult("node-a", &ipcpb.ActivatePushTargetsResult{
		StreamName: streamName, SourceGeneration: generation, TargetRevision: 5, ActivationAttempt: "attempt-new",
		Targets: []*ipcpb.PushTargetConvergence{{TargetId: targetID, MistPushId: 22, Active: true}},
	}); err != nil {
		t.Fatal(err)
	}
	capture, client := startDecklogCapture(t)
	p.decklogClient = client
	_, _, err := p.handleRestreamStatus(&ipcpb.MistTrigger{
		NodeId: "node-a", TriggerType: "RESTREAM_STATUS_FINAL", TenantId: proto.String(tenantID),
		TriggerPayload: &ipcpb.MistTrigger_RestreamStatus{RestreamStatus: &ipcpb.PushTargetStatusReport{
			TargetId: targetID, TenantId: tenantID, StreamId: streamID, StreamName: streamName,
			SourceGeneration: generation, TargetRevision: 5, ActivationAttempt: "attempt-old", MistPushId: 21,
			State: ipcpb.RestreamState_RESTREAM_STATE_IDLE, Reason: ipcpb.RestreamReason_RESTREAM_REASON_COMPLETED,
			SourceEventId: "old-attempt-final",
		}},
	})
	if err != nil {
		t.Fatalf("replayed prior-attempt final: %v", err)
	}
	if got := capacity.CountViewers(tenantID); got != 1 {
		t.Fatalf("prior-attempt final released replacement capacity: %d", got)
	}
	tracked, found := lookupPushTargetID(streamName, targetID, generation, 5, "attempt-new")
	if !found || !tracked.Current || tracked.MistPushID != 22 {
		t.Fatalf("replacement attempt was retired: found=%v tracked=%+v", found, tracked)
	}
	if got := capture.received(); len(got) != 1 || got[0].GetRestreamStatus().GetSourceEventId() != "old-attempt-final" {
		t.Fatalf("prior-attempt billing fact was not published: %+v", got)
	}
}

func TestAttemptlessFinalCannotBindUnboundReplacement(t *testing.T) {
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })
	const (
		streamName = "live+attemptless-replay"
		tenantID   = "75111111-1111-4111-8111-111111111111"
		streamID   = "75222222-2222-4222-8222-222222222222"
		targetID   = "75333333-3333-4333-8333-333333333333"
		generation = "75444444-4444-4444-8444-444444444444"
	)
	untrackPushTargets(streamName)
	t.Cleanup(func() { untrackPushTargets(streamName) })
	p := newTestProcessor(t)
	activation := &ipcpb.ActivatePushTargets{
		StreamName: streamName, TenantId: tenantID, StreamId: streamID,
		SourceGeneration: generation, TargetRevision: 5, MaxViewers: 1,
		Targets: []*ipcpb.PushTargetSpec{{TargetId: targetID}},
	}
	if pending, err := p.reserveRestreamCapacity(context.Background(), control.AdmissionEffect{
		TenantID: tenantID, NodeID: "node-a", SourceGeneration: generation,
	}, activation, 1); err != nil || pending {
		t.Fatalf("reserve replacement: pending=%v err=%v", pending, err)
	}
	trackPushTargetsForGeneration(streamName, tenantID, streamID, generation, 5, "node-a", 1,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: "rtmp://example.test/live/key"}}, "attempt-new")

	capture, client := startDecklogCapture(t)
	p.decklogClient = client
	_, _, err := p.handleRestreamStatus(&ipcpb.MistTrigger{
		NodeId: "node-a", TriggerType: "RESTREAM_STATUS_FINAL", TenantId: proto.String(tenantID),
		TriggerPayload: &ipcpb.MistTrigger_RestreamStatus{RestreamStatus: &ipcpb.PushTargetStatusReport{
			TargetId: targetID, TenantId: tenantID, StreamId: streamID, StreamName: streamName,
			SourceGeneration: generation, TargetRevision: 5, MistPushId: 21,
			State: ipcpb.RestreamState_RESTREAM_STATE_IDLE, Reason: ipcpb.RestreamReason_RESTREAM_REASON_COMPLETED,
			SourceEventId: "attemptless-old-final",
		}},
	})
	if err != nil {
		t.Fatalf("attempt-less final: %v", err)
	}
	if got := capacity.CountViewers(tenantID); got != 1 {
		t.Fatalf("attempt-less final released replacement capacity: %d", got)
	}
	tracked, found := lookupPushTargetID(streamName, targetID, generation, 5, "attempt-new")
	if !found || !tracked.Current || tracked.MistPushID != 0 {
		t.Fatalf("attempt-less final bound or retired replacement: found=%v tracked=%+v", found, tracked)
	}
	if got := capture.received(); len(got) != 1 || got[0].GetRestreamStatus().GetSourceEventId() != "attemptless-old-final" {
		t.Fatalf("attempt-less final fact was not published: %+v", got)
	}
}

func TestTerminalActivationFromPriorAttemptCannotReleaseReplacement(t *testing.T) {
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })
	const (
		streamName = "live+attempt-terminal-replay"
		tenantID   = "81111111-1111-4111-8111-111111111111"
		streamID   = "82222222-2222-4222-8222-222222222222"
		targetID   = "83333333-3333-4333-8333-333333333333"
		generation = "84444444-4444-4444-8444-444444444444"
	)
	untrackPushTargets(streamName)
	t.Cleanup(func() { untrackPushTargets(streamName) })
	p := newTestProcessor(t)
	activation := &ipcpb.ActivatePushTargets{
		StreamName: streamName, TenantId: tenantID, StreamId: streamID,
		SourceGeneration: generation, TargetRevision: 5, MaxViewers: 1,
		Targets: []*ipcpb.PushTargetSpec{{TargetId: targetID}},
	}
	if pending, err := p.reserveRestreamCapacity(context.Background(), control.AdmissionEffect{
		TenantID: tenantID, NodeID: "node-a", SourceGeneration: generation,
	}, activation, 1); err != nil || pending {
		t.Fatalf("reserve replacement: pending=%v err=%v", pending, err)
	}
	targets := []*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: "rtmp://example.test/live/key"}}
	trackPushTargetsForGeneration(streamName, tenantID, streamID, generation, 5, "node-a", 1, targets, "attempt-old")
	trackPushTargetsForGeneration(streamName, tenantID, streamID, generation, 5, "node-a", 1, targets, "attempt-new")
	result := &ipcpb.ActivatePushTargetsResult{
		StreamName: streamName, SourceGeneration: generation, TargetRevision: 5, ActivationAttempt: "attempt-old",
		Targets: []*ipcpb.PushTargetConvergence{{
			TargetId: targetID, Reason: ipcpb.RestreamReason_RESTREAM_REASON_DESTINATION_REJECTED,
		}},
	}
	if err := p.HandlePushTargetActivationResult("node-a", result); err == nil || !strings.Contains(err.Error(), "current activation attempt") {
		t.Fatalf("prior-attempt terminal result error = %v", err)
	}
	if err := p.HandleTerminalPushTargetActivation("node-a", result); err != nil {
		t.Fatalf("ignore prior-attempt terminal cleanup: %v", err)
	}
	if got := capacity.CountViewers(tenantID); got != 1 {
		t.Fatalf("prior-attempt terminal result released replacement capacity: %d", got)
	}
	tracked, found := lookupPushTargetID(streamName, targetID, generation, 5, "attempt-new")
	if !found || !tracked.Current {
		t.Fatalf("replacement attempt was retired: found=%v tracked=%+v", found, tracked)
	}
}

func TestZeroMistIDFinalPublishesWithoutRetiringBoundRuntime(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previousDB := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(previousDB) })
	const (
		streamName = "live+zero-id-final"
		tenantID   = "81111111-1111-4111-8111-111111111111"
		streamID   = "82222222-2222-4222-8222-222222222222"
		targetID   = "83333333-3333-4333-8333-333333333333"
		generation = "84444444-4444-4444-8444-444444444444"
	)
	untrackPushTargets(streamName)
	t.Cleanup(func() { untrackPushTargets(streamName) })
	trackPushTargetsForGeneration(streamName, tenantID, streamID, generation, 6, "node-a", 0,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: "rtmp://example.test/live/key"}}, "attempt-zero")
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.admission_push_target_revisions")).
		WithArgs(targetID, int64(31), "node-a", generation, int64(6), "attempt-zero").
		WillReturnResult(sqlmock.NewResult(0, 1))
	p := newTestProcessor(t)
	if activationErr := p.HandlePushTargetActivationResult("node-a", &ipcpb.ActivatePushTargetsResult{
		StreamName: streamName, SourceGeneration: generation, TargetRevision: 6, ActivationAttempt: "attempt-zero",
		Targets: []*ipcpb.PushTargetConvergence{{TargetId: targetID, MistPushId: 31, Active: true}},
	}); activationErr != nil {
		t.Fatal(activationErr)
	}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.push_target_status_outbox")).
		WithArgs(targetID, tenantID, "idle", pushstatusoutbox.ReasonStopped, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	capture, client := startDecklogCapture(t)
	p.decklogClient = client
	_, _, err = p.handleRestreamStatus(&ipcpb.MistTrigger{
		NodeId: "node-a", TriggerType: "RESTREAM_STATUS_FINAL", TenantId: proto.String(tenantID),
		TriggerPayload: &ipcpb.MistTrigger_RestreamStatus{RestreamStatus: &ipcpb.PushTargetStatusReport{
			TargetId: targetID, TenantId: tenantID, StreamId: streamID, StreamName: streamName,
			SourceGeneration: generation, TargetRevision: 6, ActivationAttempt: "attempt-zero",
			State: ipcpb.RestreamState_RESTREAM_STATE_IDLE, Reason: ipcpb.RestreamReason_RESTREAM_REASON_STOPPED,
			SourceEventId: "zero-id-final",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tracked, found := lookupPushTargetID(streamName, targetID, generation, 6, "attempt-zero")
	if !found || !tracked.Current || tracked.MistPushID != 31 {
		t.Fatalf("zero-ID final changed current runtime: found=%v tracked=%+v", found, tracked)
	}
	if got := capture.received(); len(got) != 1 {
		t.Fatalf("zero-ID final was not published: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestZeroMistIDCompletedFinalRearmsCurrentAttempt(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previousDB := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(previousDB) })
	const (
		streamName = "live+zero-id-rearm"
		internal   = "zero-id-rearm"
		tenantID   = "b1111111-1111-4111-8111-111111111111"
		streamID   = "b2222222-2222-4222-8222-222222222222"
		targetID   = "b3333333-3333-4333-8333-333333333333"
		generation = "b4444444-4444-4444-8444-444444444444"
		attempt    = "b5555555-5555-4555-8555-555555555555"
		stateKey   = "zero-id-rearm-state-key"
	)
	if configErr := control.ConfigureAdmissionEffectEncryption(stateKey); configErr != nil {
		t.Fatal(configErr)
	}
	activation := &ipcpb.ActivatePushTargets{
		TenantId: tenantID, StreamId: streamID, StreamName: streamName,
		SourceGeneration: generation, TargetRevision: 8, ActivationAttempt: attempt,
		Targets: []*ipcpb.PushTargetSpec{{TargetId: targetID, TargetUri: "rtmp://example.test/live/key"}},
	}
	raw, err := proto.Marshal(activation)
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := fieldcrypto.DeriveFieldEncryptor([]byte(stateKey), "foghorn-ingest-admission-push-targets-v1")
	if err != nil {
		t.Fatal(err)
	}
	protected, err := encryptor.EncryptWithAAD(string(raw), []byte("foghorn:ingest-admission:push-targets\x00"+tenantID+"\x00"+internal+"\x00"+generation))
	if err != nil {
		t.Fatal(err)
	}
	untrackPushTargets(streamName)
	t.Cleanup(func() { untrackPushTargets(streamName) })
	trackPushTargetsForGeneration(streamName, tenantID, streamID, generation, 8, "node-a", 0,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: activation.Targets[0].TargetUri}}, attempt)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT effect\.id, effect\.push_targets`).
		WithArgs(tenantID, internal, generation, int64(8)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "push_targets"}).AddRow(int64(1), []byte(protected)))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.admission_push_target_revisions")).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), generation, int64(8)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE foghorn.ingest_admission_effects AS effect")).
		WithArgs(sqlmock.AnyArg(), tenantID, internal, generation, int64(8)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.push_target_status_outbox")).
		WithArgs(targetID, tenantID, "retrying", pushstatusoutbox.ReasonCompleted, nil, sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))

	p := newTestProcessor(t)
	capture, client := startDecklogCapture(t)
	p.decklogClient = client
	_, _, err = p.handleRestreamStatus(&ipcpb.MistTrigger{
		NodeId: "node-a", TriggerType: string(mist.TriggerRestreamStatusFinal), TenantId: proto.String(tenantID),
		TriggerPayload: &ipcpb.MistTrigger_RestreamStatus{RestreamStatus: &ipcpb.PushTargetStatusReport{
			TargetId: targetID, TenantId: tenantID, StreamId: streamID, StreamName: streamName,
			SourceGeneration: generation, TargetRevision: 8, ActivationAttempt: attempt,
			State: ipcpb.RestreamState_RESTREAM_STATE_IDLE, Reason: ipcpb.RestreamReason_RESTREAM_REASON_COMPLETED,
			SourceEventId: "zero-id-completed",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := capture.received(); len(got) != 1 {
		t.Fatalf("zero-ID completed final was not published: %+v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRetryableNonFinalFailureDoesNotRetireTarget(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previousDB := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(previousDB) })
	const (
		streamName = "live+retryable-failed"
		tenantID   = "91111111-1111-4111-8111-111111111111"
		streamID   = "92222222-2222-4222-8222-222222222222"
		targetID   = "93333333-3333-4333-8333-333333333333"
		generation = "94444444-4444-4444-8444-444444444444"
	)
	untrackPushTargets(streamName)
	t.Cleanup(func() { untrackPushTargets(streamName) })
	trackPushTargetsForGeneration(streamName, tenantID, streamID, generation, 7, "node-a", 0,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: "rtmp://example.test/live/key"}}, "attempt-retry")
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.push_target_status_outbox")).
		WithArgs(targetID, tenantID, "failed", pushstatusoutbox.ReasonProcessError, sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	p := newTestProcessor(t)
	_, _, err = p.handleRestreamStatus(&ipcpb.MistTrigger{
		NodeId: "node-a",
		TriggerPayload: &ipcpb.MistTrigger_RestreamStatus{RestreamStatus: &ipcpb.PushTargetStatusReport{
			TargetId: targetID, TenantId: tenantID, StreamId: streamID, StreamName: streamName,
			SourceGeneration: generation, TargetRevision: 7, ActivationAttempt: "attempt-retry",
			State: ipcpb.RestreamState_RESTREAM_STATE_FAILED, Reason: ipcpb.RestreamReason_RESTREAM_REASON_PROCESS_ERROR,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	tracked, found := lookupPushTargetID(streamName, targetID, generation, 7, "attempt-retry")
	if !found || !tracked.Current {
		t.Fatalf("retryable non-final failure retired target: found=%v tracked=%+v", found, tracked)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRetiredRestreamStatusCannotOverwriteSuccessorGeneration(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previousDB := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(previousDB) })

	const (
		streamName = "live+successor-status"
		tenantID   = "55555555-5555-4555-8555-555555555555"
		streamID   = "66666666-6666-4666-8666-666666666666"
		targetID   = "77777777-7777-4777-8777-777777777777"
		oldGen     = "88888888-8888-4888-8888-888888888888"
		newGen     = "99999999-9999-4999-8999-999999999999"
	)
	untrackPushTargets(streamName)
	t.Cleanup(func() { untrackPushTargets(streamName) })
	trackPushTargetsForGeneration(streamName, tenantID, streamID, oldGen, 3, "node-a", 0,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: "rtmp://example.test/old"}})
	trackPushTargetsForGeneration(streamName, tenantID, streamID, newGen, 4, "node-a", 0,
		[]*commodorepb.PushTargetInternal{{Id: targetID, TargetUri: "rtmp://example.test/new"}})

	p := newTestProcessor(t)
	_, _, err = p.handleRestreamStatus(&ipcpb.MistTrigger{
		NodeId: "node-a",
		TriggerPayload: &ipcpb.MistTrigger_RestreamStatus{RestreamStatus: &ipcpb.PushTargetStatusReport{
			TargetId: targetID, TenantId: tenantID, StreamId: streamID, StreamName: streamName,
			SourceGeneration: oldGen, TargetRevision: 3,
			State: ipcpb.RestreamState_RESTREAM_STATE_IDLE, Reason: ipcpb.RestreamReason_RESTREAM_REASON_STOPPED,
		}},
	})
	if err != nil {
		t.Fatalf("handle superseded status: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRestartRestoredSupersededRevisionCannotReleaseCurrentCapacity(t *testing.T) {
	capacity := state.ResetDefaultTenantCapacityForTests()
	t.Cleanup(func() { state.ResetDefaultTenantCapacityForTests() })
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	previousDB := control.GetDB()
	control.SetDB(db)
	t.Cleanup(func() { control.SetDB(previousDB) })

	const (
		streamName = "live+restart-fence"
		tenantID   = "a1111111-1111-4111-8111-111111111111"
		streamID   = "a2222222-2222-4222-8222-222222222222"
		targetID   = "a3333333-3333-4333-8333-333333333333"
		generation = "a4444444-4444-4444-8444-444444444444"
		oldAttempt = "a5555555-5555-4555-8555-555555555555"
	)
	p := newTestProcessor(t)
	current := &ipcpb.ActivatePushTargets{
		TenantId: tenantID, StreamId: streamID, StreamName: streamName,
		SourceGeneration: generation, TargetRevision: 2, MaxViewers: 1,
		Targets: []*ipcpb.PushTargetSpec{{TargetId: targetID}},
	}
	if pending, reserveErr := p.reserveRestreamCapacity(context.Background(), control.AdmissionEffect{
		TenantID: tenantID, NodeID: "node-a", SourceGeneration: generation,
	}, current, 1); reserveErr != nil || pending {
		t.Fatalf("reserve current revision: pending=%v err=%v", pending, reserveErr)
	}

	old := proto.Clone(current).(*ipcpb.ActivatePushTargets)
	old.TargetRevision = 1
	old.ActivationAttempt = oldAttempt
	raw, err := proto.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	const stateKey = "restart-fence-test-key"
	if configureErr := control.ConfigureAdmissionEffectEncryption(stateKey); configureErr != nil {
		t.Fatal(configureErr)
	}
	encryptor, err := fieldcrypto.DeriveFieldEncryptor([]byte(stateKey), "foghorn-ingest-admission-push-targets-v1")
	if err != nil {
		t.Fatal(err)
	}
	aad := []byte("foghorn:ingest-admission:push-targets\x00" + tenantID + "\x00restart-fence\x00" + generation)
	protected, err := encryptor.EncryptWithAAD(string(raw), aad)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery(`FROM foghorn\.admission_push_target_revisions AS history`).
		WithArgs(tenantID, generation, int64(1), oldAttempt).
		WillReturnRows(sqlmock.NewRows([]string{
			"tenant_id", "stream_internal_name", "node_id", "push_targets", "mist_push_ids", "target_revision", "activation_attempt", "latest_target_revision", "source_live",
		}).AddRow(tenantID, "restart-fence", "node-a", []byte(protected), []byte(`{}`), int64(1), oldAttempt, int64(2), true))

	_, _, err = p.handleRestreamStatus(&ipcpb.MistTrigger{
		NodeId: "node-a",
		TriggerPayload: &ipcpb.MistTrigger_RestreamStatus{RestreamStatus: &ipcpb.PushTargetStatusReport{
			TargetId: targetID, TenantId: tenantID, StreamId: streamID, StreamName: streamName,
			SourceGeneration: generation, TargetRevision: 1, ActivationAttempt: oldAttempt,
			State: ipcpb.RestreamState_RESTREAM_STATE_IDLE, Reason: ipcpb.RestreamReason_RESTREAM_REASON_STOPPED,
		}},
	})
	if err != nil {
		t.Fatalf("handle restored superseded status: %v", err)
	}
	if got := capacity.CountViewers(tenantID); got != 1 {
		t.Fatalf("superseded revision released current revision capacity: count=%d", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
