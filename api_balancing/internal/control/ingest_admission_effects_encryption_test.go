package control

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	fieldcrypto "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
)

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
		"prior_owner_node_id", "prior_owner_source_generation", "push_targets", "broadcast_live", "decklog_trigger",
		"peer_clusters", "drain_done", "activation_done", "broadcast_done", "decklog_done", "state", "lease_token",
	}
	mock.ExpectQuery(`WITH candidates AS`).WillReturnRows(sqlmock.NewRows(columns).
		AddRow(int64(1), testAdmissionTenantOne, "live+one", "node-1", testAdmissionGenerationOne, int64(1), "", "", substituted, false, []byte(nil), "[]", true, false, true, true, admissionStatePendingV2, "lease-1").
		AddRow(int64(2), testAdmissionTenantTwo, "live+two", "node-2", testAdmissionGenerationTwo, int64(2), "", "", valid, false, []byte(nil), "[]", true, false, true, true, admissionStatePendingV2, "lease-2"))

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
