package nodeidentity

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func registrationFixture() *ipcpb.Register {
	machineID := strings.Repeat("a", 64)
	macs := strings.Repeat("b", 64)
	return &ipcpb.Register{
		NodeId: "edge-1",
		Fingerprint: &ipcpb.NodeFingerprint{
			MachineIdSha256: &machineID,
			MacsSha256:      &macs,
		},
	}
}

func TestLoadOrCreatePrivateKeyPersistsProtectedIdentity(t *testing.T) {
	root := t.TempDir()
	first, firstStatus, err := LoadOrCreatePrivateKey(root, "edge-1", "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if firstStatus != LoadStatusCreated {
		t.Fatalf("first status = %q, want created", firstStatus)
	}
	second, secondStatus, err := LoadOrCreatePrivateKey(root, "edge-1", "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	if secondStatus != LoadStatusLoaded {
		t.Fatalf("second status = %q, want loaded", secondStatus)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("node identity changed across reload")
	}
	info, err := os.Stat(filepath.Join(root, identityDirectory, identityFile))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("identity permissions = %o, want 600", got)
	}
}

func TestLoadOrCreatePrivateKeyRejectsExposedSeed(t *testing.T) {
	root := t.TempDir()
	if _, _, err := LoadOrCreatePrivateKey(root, "edge-1", "", false, ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, identityDirectory, identityFile)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreatePrivateKey(root, "edge-1", "", false, ""); err == nil {
		t.Fatal("world-readable node identity was accepted")
	}
}

func TestLoadOrCreatePrivateKeyRejectsClonedNodeBinding(t *testing.T) {
	root := t.TempDir()
	if _, _, err := LoadOrCreatePrivateKey(root, "edge-1", "", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreatePrivateKey(root, "edge-2", "", false, ""); err == nil {
		t.Fatal("identity bound to edge-1 was accepted by edge-2")
	}
}

func TestLoadOrCreatePrivateKeyMigratesLegacyMediaSeed(t *testing.T) {
	stateRoot, mediaRoot := t.TempDir(), t.TempDir()
	legacyDir := filepath.Join(mediaRoot, legacyDirectory)
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	seed := bytes.Repeat([]byte{7}, 32)
	if err := os.WriteFile(filepath.Join(legacyDir, legacyIdentityFile), seed, 0o600); err != nil {
		t.Fatal(err)
	}
	key, status, err := LoadOrCreatePrivateKey(stateRoot, "edge-1", mediaRoot, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if status != LoadStatusMigrated || !bytes.Equal(key.Seed(), seed) {
		t.Fatalf("migration status/key mismatch: status=%q", status)
	}
	if _, err := os.Stat(filepath.Join(legacyDir, legacyIdentityFile)); !os.IsNotExist(err) {
		t.Fatalf("legacy identity was not removed after migration: %v", err)
	}
}

func TestLoadOrCreatePrivateKeyRejectsUnsafeLegacySeedWithoutReplacingIt(t *testing.T) {
	stateRoot, mediaRoot := t.TempDir(), t.TempDir()
	legacyDir := filepath.Join(mediaRoot, legacyDirectory)
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(legacyDir, legacyIdentityFile)
	seed := bytes.Repeat([]byte{8}, ed25519.SeedSize)
	if err := os.WriteFile(legacyPath, seed, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadOrCreatePrivateKey(stateRoot, "edge-1", mediaRoot, false, ""); err == nil {
		t.Fatal("unsafe legacy identity was silently replaced")
	}
	got, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("unsafe legacy identity was removed: %v", err)
	}
	if !bytes.Equal(got, seed) {
		t.Fatal("unsafe legacy identity was modified")
	}
	if _, err := os.Stat(filepath.Join(stateRoot, identityDirectory, identityFile)); !os.IsNotExist(err) {
		t.Fatalf("new identity was created despite unsafe legacy state: %v", err)
	}
}

func TestLoadOrCreatePrivateKeyRotatesOncePerRequestedTransition(t *testing.T) {
	root := t.TempDir()
	first, _, err := LoadOrCreatePrivateKey(root, "edge-1", "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	rotated, status, err := LoadOrCreatePrivateKey(root, "edge-1", "", true, "token-1")
	if err != nil {
		t.Fatal(err)
	}
	if status != LoadStatusRotated || bytes.Equal(first.Public().(ed25519.PublicKey), rotated.Public().(ed25519.PublicKey)) {
		t.Fatalf("rotation did not replace the public key: status=%q", status)
	}
	retry, retryStatus, err := LoadOrCreatePrivateKey(root, "edge-1", "", true, "token-1")
	if err != nil {
		t.Fatal(err)
	}
	if retryStatus != LoadStatusRotationPending || !bytes.Equal(rotated, retry) {
		t.Fatal("a retry while rotation remained requested minted another identity")
	}
	if _, pendingStatus, loadErr := LoadOrCreatePrivateKey(root, "edge-1", "", false, ""); loadErr != nil || pendingStatus != LoadStatusRotationPending {
		t.Fatalf("unacknowledged rotation did not remain pending: status=%q err=%v", pendingStatus, loadErr)
	}
	if completeErr := CompleteRotation(root, "edge-1"); completeErr != nil {
		t.Fatalf("complete rotation: %v", completeErr)
	}
	reused, reusedStatus, err := LoadOrCreatePrivateKey(root, "edge-1", "", true, "token-1")
	if err != nil || reusedStatus != LoadStatusLoaded || !bytes.Equal(rotated, reused) {
		t.Fatalf("completed rotation repeated for the same request: status=%q err=%v", reusedStatus, err)
	}
	second, status, err := LoadOrCreatePrivateKey(root, "edge-1", "", true, "token-2")
	if err != nil {
		t.Fatal(err)
	}
	if status != LoadStatusRotated || bytes.Equal(rotated, second) {
		t.Fatal("a later explicit rotation did not mint another identity")
	}
}

func TestLoadOrCreatePrivateKeyRejectsRotationWithoutRequestIdentity(t *testing.T) {
	root := t.TempDir()
	if _, _, err := LoadOrCreatePrivateKey(root, "edge-1", "", true, ""); err == nil {
		t.Fatal("rotation without an enrollment request identity was accepted")
	}
}

func TestRegistrationProofBindsIdentityFingerprintAndFreshness(t *testing.T) {
	privateKey, _, err := LoadOrCreatePrivateKey(t.TempDir(), "edge-1", "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	register := registrationFixture()
	if err := SignRegistration(register, privateKey, now); err != nil {
		t.Fatal(err)
	}
	if err := VerifyRegistration(register, now.Add(time.Second)); err != nil {
		t.Fatalf("valid proof rejected: %v", err)
	}

	tampered := registrationFixture()
	tampered.NodeId = "edge-2"
	tampered.NodeIdentityPublicKeyEd25519 = append([]byte(nil), register.GetNodeIdentityPublicKeyEd25519()...)
	tampered.NodeIdentityProofNonce = append([]byte(nil), register.GetNodeIdentityProofNonce()...)
	tampered.NodeIdentityProofIssuedAt = register.GetNodeIdentityProofIssuedAt()
	tampered.NodeIdentityProofEd25519 = append([]byte(nil), register.GetNodeIdentityProofEd25519()...)
	if err := VerifyRegistration(tampered, now); err == nil {
		t.Fatal("proof was valid for a different asserted node")
	}
	register.NodeIdentityRotationRequested = true
	if err := VerifyRegistration(register, now); err == nil {
		t.Fatal("proof was valid after enabling an unsigned identity-rotation request")
	}
	register.NodeIdentityRotationRequested = false
	if err := VerifyRegistration(register, now.Add(MaxProofAge+time.Second)); err == nil {
		t.Fatal("stale proof was accepted")
	}
}
