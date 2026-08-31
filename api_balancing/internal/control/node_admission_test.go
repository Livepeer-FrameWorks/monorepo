package control

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/nodeidentity"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestStableNodeFingerprintDigestUsesOnlyStableSignals(t *testing.T) {
	base := &ipcpb.NodeFingerprint{
		LocalIpv4:       []string{"10.0.0.2"},
		MacsSha256:      stringPointerForControlTest("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		MachineIdSha256: stringPointerForControlTest("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"),
	}
	first, err := stableNodeFingerprintDigest(base, quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_MACHINE_ID)
	if err != nil {
		t.Fatal(err)
	}
	moved := &ipcpb.NodeFingerprint{
		LocalIpv4:       []string{"192.0.2.50"},
		LocalIpv6:       []string{"2001:db8::50"},
		MacsSha256:      stringPointerForControlTest("cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"),
		MachineIdSha256: stringPointerForControlTest("BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"),
	}
	second, err := stableNodeFingerprintDigest(moved, quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_MACHINE_ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("IP/MAC movement or digest casing changed machine-id identity")
	}

	changed := &ipcpb.NodeFingerprint{MachineIdSha256: stringPointerForControlTest("cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")}
	third, err := stableNodeFingerprintDigest(changed, quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_MACHINE_ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) == string(third) {
		t.Fatal("different stable identities produced the same admission digest")
	}
	macMatched, err := stableNodeFingerprintDigest(base, quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_MACS)
	if err != nil {
		t.Fatal(err)
	}
	changedMachineSameMAC := &ipcpb.NodeFingerprint{
		MachineIdSha256: stringPointerForControlTest("cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"),
		MacsSha256:      base.MacsSha256,
	}
	macMatchedAgain, err := stableNodeFingerprintDigest(changedMachineSameMAC, quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_MACS)
	if err != nil {
		t.Fatal(err)
	}
	if string(macMatched) != string(macMatchedAgain) {
		t.Fatal("a MAC-authenticated resolution persisted an unrelated machine-id signal")
	}
	if _, err := stableNodeFingerprintDigest(base, quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_PEER_IP); err == nil {
		t.Fatal("peer-IP resolution was accepted as durable admission authority")
	}
	if _, err := stableNodeFingerprintDigest(&ipcpb.NodeFingerprint{LocalIpv4: []string{"10.0.0.2"}}, quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_UNSPECIFIED); err == nil {
		t.Fatal("IP-only fingerprint was accepted for offline authentication")
	}
	if _, err := stableNodeFingerprintDigest(&ipcpb.NodeFingerprint{MachineIdSha256: stringPointerForControlTest("not-sha256")}, quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_MACHINE_ID); err == nil {
		t.Fatal("malformed stable fingerprint was accepted")
	}
}

func TestNodeIdentityAuthorityUnavailableDoesNotMaskAuthoritativeRejection(t *testing.T) {
	if !nodeIdentityAuthorityUnavailable(status.Error(codes.Unavailable, "down"), true) {
		t.Fatal("Unavailable was not treated as an authority outage")
	}
	if !nodeIdentityAuthorityUnavailable(context.DeadlineExceeded, true) {
		t.Fatal("deadline was not treated as an authority outage")
	}
	if nodeIdentityAuthorityUnavailable(status.Error(codes.NotFound, "unknown fingerprint"), true) {
		t.Fatal("authoritative unknown fingerprint was treated as an outage")
	}
	if nodeIdentityAuthorityUnavailable(status.Error(codes.PermissionDenied, "revoked"), true) {
		t.Fatal("authoritative rejection was treated as an outage")
	}
	if nodeIdentityAuthorityUnavailable(nil, true) {
		t.Fatal("an empty authoritative response was treated as an outage")
	}
	if !nodeIdentityAuthorityUnavailable(nil, false) {
		t.Fatal("an absent authority client did not permit a previously persisted admission")
	}
}

func TestDurableNodeAdmissionRoundTrip(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := db
	db = mockDB
	t.Cleanup(func() {
		db = previousDB
		_ = mockDB.Close()
	})
	fingerprint := &ipcpb.NodeFingerprint{MachineIdSha256: stringPointerForControlTest("dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")}
	publicKey, _, keyErr := ed25519.GenerateKey(rand.Reader)
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	register := &ipcpb.Register{Fingerprint: fingerprint, NodeIdentityPublicKeyEd25519: publicKey}

	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM foghorn.node_admissions")).
		WithArgs("edge-1", sqlmock.AnyArg(), publicKey).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("INSERT INTO foghorn.node_admissions")).
		WithArgs("edge-1", sqlmock.AnyArg(), publicKey, "5eed517e-ba5e-da7a-517e-ba5eda7a0001", "media-a", sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"canonical_node_id"}).AddRow("edge-1"))
	mock.ExpectCommit()
	if persistErr := persistDurableNodeAdmission(context.Background(), "edge-1", "5eed517e-ba5e-da7a-517e-ba5eda7a0001", "media-a", register, quartermasterpb.NodeFingerprintMatchSource_NODE_FINGERPRINT_MATCH_SOURCE_MACHINE_ID); persistErr != nil {
		t.Fatal(persistErr)
	}

	mock.ExpectQuery(regexp.QuoteMeta("SELECT canonical_node_id, tenant_id, cluster_id, public_key_ed25519, validated_at, valid_until")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"canonical_node_id", "tenant_id", "cluster_id", "public_key_ed25519", "validated_at", "valid_until"}).
			AddRow("edge-1", "5eed517e-ba5e-da7a-517e-ba5eda7a0001", "media-a", publicKey, time.Now(), time.Now().Add(time.Hour)))
	admission, err := loadDurableNodeAdmission(context.Background(), register)
	if err != nil {
		t.Fatal(err)
	}
	if admission.canonicalNodeID != "edge-1" || admission.tenantID != "5eed517e-ba5e-da7a-517e-ba5eda7a0001" || admission.clusterID != "media-a" {
		t.Fatalf("unexpected durable admission: %+v", admission)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadDurableNodeAdmissionRejectsUnknownFingerprint(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := db
	db = mockDB
	t.Cleanup(func() {
		db = previousDB
		_ = mockDB.Close()
	})
	fingerprint := &ipcpb.NodeFingerprint{MachineIdSha256: stringPointerForControlTest("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")}
	publicKey, _, keyErr := ed25519.GenerateKey(rand.Reader)
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	register := &ipcpb.Register{Fingerprint: fingerprint, NodeIdentityPublicKeyEd25519: publicKey}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT canonical_node_id, tenant_id, cluster_id, public_key_ed25519, validated_at, valid_until")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnError(errors.New("lookup failed"))
	if _, err := loadDurableNodeAdmission(context.Background(), register); err == nil {
		t.Fatal("unknown fingerprint was accepted")
	}
}

func TestLoadDurableNodeAdmissionRejectsPinnedKeyMismatch(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := db
	db = mockDB
	t.Cleanup(func() {
		db = previousDB
		_ = mockDB.Close()
	})
	fingerprint := &ipcpb.NodeFingerprint{MachineIdSha256: stringPointerForControlTest("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")}
	presentedKey, _, keyErr := ed25519.GenerateKey(rand.Reader)
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	pinnedKey, _, keyErr := ed25519.GenerateKey(rand.Reader)
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	register := &ipcpb.Register{Fingerprint: fingerprint, NodeIdentityPublicKeyEd25519: presentedKey}
	mock.ExpectQuery(regexp.QuoteMeta("SELECT canonical_node_id, tenant_id, cluster_id, public_key_ed25519, validated_at, valid_until")).
		WithArgs(sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"canonical_node_id", "tenant_id", "cluster_id", "public_key_ed25519", "validated_at", "valid_until"}).
			AddRow("edge-1", "5eed517e-ba5e-da7a-517e-ba5eda7a0001", "media-a", pinnedKey, time.Now(), time.Now().Add(time.Hour)))
	if _, err := loadDurableNodeAdmission(context.Background(), register); err == nil {
		t.Fatal("durable admission accepted a public key different from its offline pin")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConsumeNodeIdentityProofRejectsReplay(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	previousDB := db
	db = mockDB
	t.Cleanup(func() {
		db = previousDB
		_ = mockDB.Close()
	})
	_, privateKey, keyErr := ed25519.GenerateKey(rand.Reader)
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	now := time.Now().UTC()
	register := &ipcpb.Register{
		NodeId: "edge-replay",
		Fingerprint: &ipcpb.NodeFingerprint{
			MachineIdSha256: stringPointerForControlTest("abababababababababababababababababababababababababababababababab"),
		},
	}
	if err := nodeidentity.SignRegistration(register, privateKey, now); err != nil {
		t.Fatal(err)
	}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.node_admission_proof_nonces")).
		WithArgs(sqlmock.AnyArg(), register.GetNodeIdentityProofNonce(), sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 0))
	if err := consumeNodeIdentityProof(context.Background(), register); err == nil {
		t.Fatal("replayed node identity nonce was accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func stringPointerForControlTest(value string) *string { return &value }
