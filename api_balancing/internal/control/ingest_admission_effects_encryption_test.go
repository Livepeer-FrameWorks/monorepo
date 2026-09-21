package control

import (
	"bytes"
	"context"
	"database/sql/driver"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	fieldcrypto "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/lib/pq"
	"google.golang.org/protobuf/proto"
)

type protectedActivationMatcher struct {
	tenantID, internalName, generation string
	decoded                            ipcpb.ActivatePushTargets
	err                                error
}

func (m *protectedActivationMatcher) Match(value driver.Value) bool {
	protected, ok := value.([]byte)
	if !ok {
		return false
	}
	opened, err := openAdmissionPushTargets(protected, m.tenantID, m.internalName, m.generation, true)
	if err == nil {
		err = proto.Unmarshal(opened, &m.decoded)
	}
	m.err = err
	return err == nil
}

const (
	testAdmissionTenantOne       = "10000000-0000-0000-0000-000000000001"
	testAdmissionTenantTwo       = "20000000-0000-0000-0000-000000000002"
	testAdmissionTenantOther     = "30000000-0000-0000-0000-000000000003"
	testAdmissionGenerationOne   = "40000000-0000-0000-0000-000000000001"
	testAdmissionGenerationTwo   = "40000000-0000-0000-0000-000000000002"
	testAdmissionGenerationOther = "40000000-0000-0000-0000-000000000003"
)

func TestAdmissionPushTargetsEncryptedAtRestAndOpenedForDelivery(t *testing.T) {
	previous := admissionEffectEncryptor
	t.Cleanup(func() { admissionEffectEncryptor = previous })
	if err := ConfigureAdmissionEffectEncryption("test-foghorn-state-key"); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"target_uri":"rtmp://example.test/live/secret-stream-key"}`)
	stored, err := protectAdmissionPushTargets(raw, testAdmissionTenantOne, "stream-1", testAdmissionGenerationOne)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("secret-stream-key")) || !fieldcrypto.IsEncrypted(string(stored)) {
		t.Fatalf("push-target credential was not encrypted at rest: %q", stored)
	}
	opened, err := openAdmissionPushTargets(stored, testAdmissionTenantOne, "stream-1", testAdmissionGenerationOne, true)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, raw) {
		t.Fatalf("opened payload = %q, want %q", opened, raw)
	}
}

func TestRecordAdmissionPushTargetDispatchPersistsExactAdmittedSubset(t *testing.T) {
	previousEncryptor := admissionEffectEncryptor
	t.Cleanup(func() { admissionEffectEncryptor = previousEncryptor })
	if err := ConfigureAdmissionEffectEncryption("test-foghorn-state-key"); err != nil {
		t.Fatal(err)
	}
	const internalName = "live+capacity-subset"
	const attempt = "70000000-0000-4000-8000-000000000007"
	activation := &ipcpb.ActivatePushTargets{
		StreamName: internalName, SourceGeneration: testAdmissionGenerationOne,
		TargetRevision: 7, ActivationAttempt: attempt,
		Targets: []*ipcpb.PushTargetSpec{{TargetId: "admitted", TargetUri: "rtmp://example.test/live/admitted"}},
	}
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := db
	SetDB(mockDB)
	t.Cleanup(func() { SetDB(previousDB); _ = mockDB.Close() })
	matcher := &protectedActivationMatcher{
		tenantID: testAdmissionTenantOne, internalName: internalName, generation: testAdmissionGenerationOne,
	}
	mock.ExpectExec(`UPDATE foghorn.admission_push_target_revisions`).
		WithArgs(matcher, testAdmissionGenerationOne, int64(7), attempt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := RecordAdmissionPushTargetDispatch(context.Background(), testAdmissionTenantOne, internalName, testAdmissionGenerationOne, activation); err != nil {
		t.Fatal(err)
	}
	if matcher.err != nil {
		t.Fatal(matcher.err)
	}
	if len(matcher.decoded.GetTargets()) != 1 || matcher.decoded.GetTargets()[0].GetTargetId() != "admitted" {
		t.Fatalf("durable dispatch snapshot = %+v, want exact admitted target", matcher.decoded.GetTargets())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCapacityRearmSkipsPoisonedRowAndProcessesValidRow(t *testing.T) {
	previousEncryptor := admissionEffectEncryptor
	t.Cleanup(func() { admissionEffectEncryptor = previousEncryptor })
	if err := ConfigureAdmissionEffectEncryption("test-foghorn-state-key"); err != nil {
		t.Fatal(err)
	}
	valid := &ipcpb.ActivatePushTargets{
		StreamName: "live+valid", SourceGeneration: testAdmissionGenerationTwo,
		TargetRevision: 8, ActivationAttempt: "70000000-0000-4000-8000-000000000008",
		Targets: []*ipcpb.PushTargetSpec{{TargetId: "valid-target", TargetUri: "rtmp://example.test/live/valid"}},
	}
	raw, err := proto.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	protected, err := protectAdmissionPushTargets(raw, testAdmissionTenantTwo, valid.GetStreamName(), testAdmissionGenerationTwo)
	if err != nil {
		t.Fatal(err)
	}
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := db
	SetDB(mockDB)
	t.Cleanup(func() { SetDB(previousDB); _ = mockDB.Close() })
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock`).
		WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(true))
	mock.ExpectQuery(`FROM foghorn.ingest_admission_effects AS effect`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "stream_internal_name", "source_generation", "target_revision", "push_targets"}).
			AddRow(int64(1), testAdmissionTenantOne, "live+poison", testAdmissionGenerationOne, int64(7), []byte("not-a-protobuf")).
			AddRow(int64(2), testAdmissionTenantTwo, valid.GetStreamName(), testAdmissionGenerationTwo, int64(8), protected))
	mock.ExpectExec(`UPDATE foghorn.admission_push_target_revisions`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), testAdmissionGenerationTwo, int64(8)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE foghorn.ingest_admission_effects AS effect`).
		WithArgs(sqlmock.AnyArg(), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	updated, err := RearmCapacityPendingPushTargetEffects(context.Background())
	if err != nil || updated != 1 {
		t.Fatalf("capacity rearm updated=%d err=%v, want valid row only", updated, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestResolveAdmissionPushTargetIdentityRestoresEncryptedAttribution(t *testing.T) {
	previousEncryptor := admissionEffectEncryptor
	t.Cleanup(func() { admissionEffectEncryptor = previousEncryptor })
	if err := ConfigureAdmissionEffectEncryption("test-foghorn-state-key"); err != nil {
		t.Fatal(err)
	}
	activation := &ipcpb.ActivatePushTargets{
		TenantId: testAdmissionTenantOne, StreamId: "50000000-0000-0000-0000-000000000001",
		StreamName: "live+stream-1", SourceGeneration: testAdmissionGenerationOne,
		TargetRevision: 9, MaxViewers: 4, ActivationAttempt: "70000000-0000-4000-8000-000000000001",
		Targets: []*ipcpb.PushTargetSpec{{TargetId: "60000000-0000-0000-0000-000000000001", Platform: "youtube", TargetUri: "rtmp://example.test/live/secret"}},
	}
	raw, err := proto.Marshal(activation)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := protectAdmissionPushTargets(raw, testAdmissionTenantOne, "stream-1", testAdmissionGenerationOne)
	if err != nil {
		t.Fatal(err)
	}
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := db
	SetDB(mockDB)
	t.Cleanup(func() { SetDB(previousDB); _ = mockDB.Close() })
	mock.ExpectQuery(`FROM foghorn.admission_push_target_revisions AS history`).
		WithArgs(testAdmissionTenantOne, testAdmissionGenerationOne, int64(9), activation.GetActivationAttempt()).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "stream_internal_name", "node_id", "push_targets", "mist_push_ids", "target_revision", "activation_attempt", "latest_target_revision", "source_live"}).
			AddRow(testAdmissionTenantOne, "stream-1", "node-a", stored, []byte(`{"60000000-0000-0000-0000-000000000001":123}`), int64(9), activation.GetActivationAttempt(), int64(10), true))

	identity, err := ResolveAdmissionPushTargetIdentity(context.Background(), testAdmissionTenantOne, testAdmissionGenerationOne, 9, activation.GetActivationAttempt(), activation.Targets[0].TargetId)
	if err != nil {
		t.Fatal(err)
	}
	if identity.TargetID != activation.Targets[0].TargetId || identity.Platform != "youtube" || !identity.SourceLive || identity.MaxViewers != 4 || identity.LatestRevision != 10 || identity.MistPushID != 123 {
		t.Fatalf("unexpected restored identity: %+v", identity)
	}
}

func TestAdmissionPushTargetsPlaintextMigrationCompatibility(t *testing.T) {
	previous := admissionEffectEncryptor
	t.Cleanup(func() { admissionEffectEncryptor = previous })
	if err := ConfigureAdmissionEffectEncryption("test-foghorn-state-key"); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"target_uri":"rtmp://legacy.example/live/key"}`)
	opened, err := openAdmissionPushTargets(legacy, testAdmissionTenantOne, "stream-1", testAdmissionGenerationOne)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, legacy) {
		t.Fatalf("legacy plaintext changed during read: %q", opened)
	}
}

func TestAdmissionPushTargetsV2StateRejectsPlaintextDowngrade(t *testing.T) {
	previous := admissionEffectEncryptor
	t.Cleanup(func() { admissionEffectEncryptor = previous })
	if err := ConfigureAdmissionEffectEncryption("test-foghorn-state-key"); err != nil {
		t.Fatal(err)
	}
	if _, err := openAdmissionPushTargets([]byte("attacker-controlled-plaintext"), testAdmissionTenantOne, "stream-1", testAdmissionGenerationOne, true); err == nil {
		t.Fatal("v2 state accepted an unprefixed payload")
	}
}

func TestAdmissionPushTargetsRejectsCrossRowCiphertextSubstitution(t *testing.T) {
	previous := admissionEffectEncryptor
	t.Cleanup(func() { admissionEffectEncryptor = previous })
	if err := ConfigureAdmissionEffectEncryption("test-foghorn-state-key"); err != nil {
		t.Fatal(err)
	}
	stored, err := protectAdmissionPushTargets([]byte("secret"), testAdmissionTenantOne, "stream-1", testAdmissionGenerationOne)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openAdmissionPushTargets(stored, testAdmissionTenantTwo, "stream-1", testAdmissionGenerationOne, true); err == nil {
		t.Fatal("row-bound ciphertext opened for another tenant")
	}
}

func TestReconcileActivePushTargetAuthorityRepairsSameRevisionCollision(t *testing.T) {
	previousEncryptor := admissionEffectEncryptor
	t.Cleanup(func() { admissionEffectEncryptor = previousEncryptor })
	if err := ConfigureAdmissionEffectEncryption("test-foghorn-state-key"); err != nil {
		t.Fatal(err)
	}
	current := &ipcpb.ActivatePushTargets{
		StreamName: "stream-1", SourceGeneration: testAdmissionGenerationOne, TargetRevision: 1,
		Targets: []*ipcpb.PushTargetSpec{{TargetId: "target-1", TargetUri: "rtmp://old.example/live/key"}},
	}
	raw, err := proto.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := protectAdmissionPushTargets(raw, testAdmissionTenantOne, "stream-1", testAdmissionGenerationOne)
	if err != nil {
		t.Fatal(err)
	}
	desired := proto.CloneOf(current)
	desired.Targets[0].TargetUri = "rtmp://new.example/live/key"

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := db
	SetDB(mockDB)
	t.Cleanup(func() { SetDB(previousDB); _ = mockDB.Close() })
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`FROM foghorn.ingest_admission_effects AS effect`).
		WithArgs(testAdmissionTenantOne, "stream-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "node_id", "source_generation", "push_targets", "state", "target_revision"}).
			AddRow(int64(7), "node-a", testAdmissionGenerationOne, stored, "applied_v2", int64(1)))
	mock.ExpectExec(`UPDATE foghorn.admission_push_target_revisions`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), testAdmissionGenerationOne, int64(1)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE foghorn.ingest_admission_effects`).
		WithArgs(sqlmock.AnyArg(), int64(1), int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	updated, err := ReconcileActivePushTargetAuthority(context.Background(), testAdmissionTenantOne, "stream-1", desired)
	if err != nil || updated != 1 {
		t.Fatalf("same-revision repair updated=%d err=%v", updated, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileActivePushTargetAuthorityReplacesPoisonWithEmptyAuthority(t *testing.T) {
	previousEncryptor := admissionEffectEncryptor
	t.Cleanup(func() { admissionEffectEncryptor = previousEncryptor })
	if err := ConfigureAdmissionEffectEncryption("test-foghorn-state-key"); err != nil {
		t.Fatal(err)
	}
	poisoned, err := protectAdmissionPushTargets([]byte("wrong owner"), testAdmissionTenantOther, "other-stream", testAdmissionGenerationOther)
	if err != nil {
		t.Fatal(err)
	}
	desired := &ipcpb.ActivatePushTargets{
		StreamName: "stream-1", SourceGeneration: testAdmissionGenerationOne, TargetRevision: 2,
	}

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := db
	SetDB(mockDB)
	t.Cleanup(func() { SetDB(previousDB); _ = mockDB.Close() })
	mock.ExpectBegin()
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).WithArgs(sqlmock.AnyArg()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`FROM foghorn.ingest_admission_effects AS effect`).
		WithArgs(testAdmissionTenantOne, "stream-1").
		WillReturnRows(sqlmock.NewRows([]string{"id", "node_id", "source_generation", "push_targets", "state", "target_revision"}).
			AddRow(int64(8), "node-a", testAdmissionGenerationOne, poisoned, "applied_v2", int64(1)))
	mock.ExpectExec(`INSERT INTO foghorn.admission_push_target_revisions`).
		WithArgs(testAdmissionTenantOne, "stream-1", "node-a", testAdmissionGenerationOne, int64(2), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE foghorn.ingest_admission_effects`).
		WithArgs(sqlmock.AnyArg(), int64(2), int64(8)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	updated, err := ReconcileActivePushTargetAuthority(context.Background(), testAdmissionTenantOne, "stream-1", desired)
	if err != nil || updated != 1 {
		t.Fatalf("poison replacement updated=%d err=%v", updated, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestClaimAdmissionEffectsIsolatesUndecryptableRowWithinBatch(t *testing.T) {
	previousEncryptor := admissionEffectEncryptor
	t.Cleanup(func() { admissionEffectEncryptor = previousEncryptor })
	if err := ConfigureAdmissionEffectEncryption("test-foghorn-state-key"); err != nil {
		t.Fatal(err)
	}
	valid, err := protectAdmissionPushTargets([]byte("valid-targets"), testAdmissionTenantTwo, "live+two", testAdmissionGenerationTwo)
	if err != nil {
		t.Fatal(err)
	}
	substituted, err := protectAdmissionPushTargets([]byte("wrong-owner"), testAdmissionTenantOther, "live+other", testAdmissionGenerationOther)
	if err != nil {
		t.Fatal(err)
	}

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := db
	SetDB(mockDB)
	t.Cleanup(func() {
		SetDB(previousDB)
		_ = mockDB.Close()
	})
	columns := []string{
		"id", "tenant_id", "stream_internal_name", "node_id", "source_generation", "source_revision",
		"prior_owner_node_id", "prior_owner_source_generation", "push_targets", "target_revision", "capacity_pending", "broadcast_live", "decklog_trigger",
		"peer_clusters", "drain_done", "activation_done", "broadcast_done", "decklog_done", "state", "lease_token",
	}
	mock.ExpectQuery(`WITH candidates AS`).WillReturnRows(sqlmock.NewRows(columns).
		AddRow(int64(1), testAdmissionTenantOne, "live+one", "node-1", testAdmissionGenerationOne, int64(1), "", "", substituted, int64(7), false, false, []byte(nil), "[]", true, false, true, true, admissionStatePendingV2, "lease-1").
		AddRow(int64(2), testAdmissionTenantTwo, "live+two", "node-2", testAdmissionGenerationTwo, int64(2), "", "", valid, int64(8), false, false, []byte(nil), "[]", true, false, true, true, admissionStatePendingV2, "lease-2"))

	effects, err := ClaimAdmissionEffects(context.Background(), 10, time.Minute, "instance-1")
	if err != nil {
		t.Fatalf("claim batch: %v", err)
	}
	if len(effects) != 2 {
		t.Fatalf("claimed %d effects, want 2", len(effects))
	}
	if !effects[0].ActivationPayloadInvalid || len(effects[0].PushTargets) != 0 {
		t.Fatalf("corrupt row was not isolated: %+v", effects[0])
	}
	if effects[1].ActivationPayloadInvalid || string(effects[1].PushTargets) != "valid-targets" {
		t.Fatalf("valid sibling was lost: %+v", effects[1])
	}
	if effects[0].TargetRevision != 7 || effects[1].TargetRevision != 8 {
		t.Fatalf("target revisions = %d, %d; want 7, 8", effects[0].TargetRevision, effects[1].TargetRevision)
	}
}

func TestAdmissionPayloadMigrationFencesLegacyRowAsV2(t *testing.T) {
	previousEncryptor := admissionEffectEncryptor
	t.Cleanup(func() { admissionEffectEncryptor = previousEncryptor })
	if err := ConfigureAdmissionEffectEncryption("test-foghorn-state-key"); err != nil {
		t.Fatal(err)
	}
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := db
	SetDB(mockDB)
	t.Cleanup(func() {
		SetDB(previousDB)
		_ = mockDB.Close()
	})
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT id, tenant_id::text`).WithArgs(int32(10)).WillReturnRows(sqlmock.NewRows([]string{
		"id", "tenant_id", "stream_internal_name", "source_generation", "push_targets", "state",
	}).AddRow(int64(7), testAdmissionTenantOne, "live+one", testAdmissionGenerationOne, []byte("legacy-targets"), "pending"))
	mock.ExpectExec(`UPDATE foghorn.ingest_admission_effects`).WithArgs(sqlmock.AnyArg(), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	count, err := migrateAdmissionEffectEncryptionBatch(context.Background(), 10)
	if err != nil || count != 1 {
		t.Fatalf("migration count=%d err=%v", count, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestCapacityRearmReplaysADatabaseAbortInsteadOfSkippingTheRow proves a serialization abort while rotating a valid
// obligation replays the batch rather than being recorded as a malformed payload.
func TestCapacityRearmReplaysADatabaseAbortInsteadOfSkippingTheRow(t *testing.T) {
	previousEncryptor := admissionEffectEncryptor
	t.Cleanup(func() { admissionEffectEncryptor = previousEncryptor })
	if err := ConfigureAdmissionEffectEncryption("test-foghorn-state-key"); err != nil {
		t.Fatal(err)
	}
	valid := &ipcpb.ActivatePushTargets{
		StreamName: "live+valid", SourceGeneration: testAdmissionGenerationTwo,
		TargetRevision: 8, ActivationAttempt: "70000000-0000-4000-8000-000000000008",
		Targets: []*ipcpb.PushTargetSpec{{TargetId: "valid-target", TargetUri: "rtmp://example.test/live/valid"}},
	}
	raw, err := proto.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	protected, err := protectAdmissionPushTargets(raw, testAdmissionTenantTwo, valid.GetStreamName(), testAdmissionGenerationTwo)
	if err != nil {
		t.Fatal(err)
	}
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := db
	SetDB(mockDB)
	t.Cleanup(func() { SetDB(previousDB); _ = mockDB.Close() })
	expectBatch := func() {
		mock.ExpectBegin()
		mock.ExpectQuery(`SELECT pg_try_advisory_xact_lock`).
			WillReturnRows(sqlmock.NewRows([]string{"acquired"}).AddRow(true))
		mock.ExpectQuery(`FROM foghorn.ingest_admission_effects AS effect`).
			WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "stream_internal_name", "source_generation", "target_revision", "push_targets"}).
				AddRow(int64(2), testAdmissionTenantTwo, valid.GetStreamName(), testAdmissionGenerationTwo, int64(8), protected))
	}
	expectBatch()
	mock.ExpectExec(`UPDATE foghorn.admission_push_target_revisions`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), testAdmissionGenerationTwo, int64(8)).
		WillReturnError(&pq.Error{Code: "40001", Message: "could not serialize access due to concurrent update"})
	mock.ExpectRollback()
	expectBatch()
	mock.ExpectExec(`UPDATE foghorn.admission_push_target_revisions`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), testAdmissionGenerationTwo, int64(8)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE foghorn.ingest_admission_effects AS effect`).
		WithArgs(sqlmock.AnyArg(), int64(2)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	updated, err := RearmCapacityPendingPushTargetEffects(context.Background())
	if err != nil || updated != 1 {
		t.Fatalf("capacity rearm updated=%d err=%v, want the row re-armed after the replay", updated, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
