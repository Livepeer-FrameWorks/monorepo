package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/nodeidentity"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"golang.org/x/sys/unix"
)

func TestTerminalEnrollmentControlErrorIncludesRotationRequired(t *testing.T) {
	for _, code := range []string{
		"ENROLLMENT_REQUIRED",
		"ENROLLMENT_FAILED",
		"ENROLLMENT_UNAVAILABLE",
		"IDENTITY_ROTATION_REQUIRED",
	} {
		if !terminalEnrollmentControlError(code) {
			t.Fatalf("%q must terminate the control connection", code)
		}
	}
	if terminalEnrollmentControlError("TRANSIENT_BACKEND_ERROR") {
		t.Fatal("transient control errors must remain retryable")
	}
}

func TestEnrollmentTokenConsumedAfterAcceptedRegistration(t *testing.T) {
	stateDir := t.TempDir()
	nodeID := "edge-node-1"
	if got := enrollmentTokenForRegistration(stateDir, nodeID, "one-time-token", false); got != "one-time-token" {
		t.Fatalf("initial token = %q", got)
	}
	if err := markEnrollmentAccepted(stateDir, nodeID); err != nil {
		t.Fatal(err)
	}
	if got := enrollmentTokenForRegistration(stateDir, nodeID, "one-time-token", false); got != "" {
		t.Fatalf("consumed token was resent: %q", got)
	}
	if _, err := os.Stat(enrollmentReceiptPath(stateDir, nodeID)); err != nil {
		t.Fatalf("durable enrollment receipt: %v", err)
	}
	if got := enrollmentTokenForRegistration(stateDir, nodeID, "rotation-token", true); got != "rotation-token" {
		t.Fatalf("rotation token = %q", got)
	}
}

func TestClearConsumedEnrollmentCredentialsScrubsTokenAndRotationFlag(t *testing.T) {
	tokenFile := t.TempDir() + "/enroll.env"
	runtimeFile := t.TempDir() + "/edge.env"
	if err := os.WriteFile(tokenFile, []byte("EDGE_ENROLLMENT_TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFile, []byte("NODE_ID=edge-1\nHELMSMAN_ROTATE_NODE_IDENTITY=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := clearConsumedEnrollmentCredentials(tokenFile, runtimeFile); err != nil {
		t.Fatal(err)
	}
	tokenData, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(tokenData) != "EDGE_ENROLLMENT_TOKEN=\n" {
		if strings.TrimSpace(strings.TrimPrefix(string(tokenData), "EDGE_ENROLLMENT_TOKEN=")) != "" {
			t.Fatalf("token file = %q", tokenData)
		}
	}
	runtimeData, err := os.ReadFile(runtimeFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(strings.TrimPrefix(strings.Split(string(runtimeData), "\n")[1], "HELMSMAN_ROTATE_NODE_IDENTITY=")) != "" {
		t.Fatalf("runtime env = %q", runtimeData)
	}
	if len(tokenData) != len("EDGE_ENROLLMENT_TOKEN=secret\n") || len(runtimeData) != len("NODE_ID=edge-1\nHELMSMAN_ROTATE_NODE_IDENTITY=true\n") {
		t.Fatal("credential scrub changed file length")
	}
}

func TestClearConsumedEnrollmentCredentialsRequiresBothConfiguredPaths(t *testing.T) {
	if err := clearConsumedEnrollmentCredentials("", "/runtime.env"); err == nil {
		t.Fatal("empty enrollment token path reported successful cleanup")
	}
	if err := clearConsumedEnrollmentCredentials("/token.env", ""); err == nil {
		t.Fatal("empty runtime environment path reported successful cleanup")
	}
}

func TestFinalizeAcceptedEnrollmentDefersSubmittedCredentialCleanupWhenPathsMissing(t *testing.T) {
	stateDir := t.TempDir()
	finalizeErr := finalizeAcceptedEnrollment(stateDir, "edge-1", "", "", false, "token-1")
	if finalizeErr == nil || !strings.Contains(finalizeErr.Error(), "runtime environment file is required") {
		t.Fatalf("deferred cleanup did not preserve the direct failure: %v", finalizeErr)
	}
	if _, err := os.Stat(enrollmentReceiptPath(stateDir, "edge-1")); err != nil {
		t.Fatalf("accepted enrollment receipt: %v", err)
	}
	if _, err := os.Stat(credentialCleanupRequestPath(stateDir)); err != nil {
		t.Fatalf("missing-path cleanup was not made visible for the root helper: %v", err)
	}
	if _, err := os.Stat(credentialCleanupCompletionPath(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("missing-path cleanup reported a durable completion: %v", err)
	}
	var deferred *credentialCleanupDeferredError
	if !errors.As(finalizeErr, &deferred) {
		t.Fatalf("successful cleanup deferral was not distinguishable from finalization failure: %T", finalizeErr)
	}
	if token := enrollmentTokenForRegistration(stateDir, "edge-1", "consumed-token", false); token != "" {
		t.Fatalf("accepted enrollment would resend its consumed token after cleanup deferral: %q", token)
	}
}

func TestFinalizeAcceptedEnrollmentDoesNotRescheduleCompletedGeneration(t *testing.T) {
	stateDir := t.TempDir()
	firstErr := finalizeAcceptedEnrollment(stateDir, "edge-1", "", "", false, "token-1")
	var deferred *credentialCleanupDeferredError
	if !errors.As(firstErr, &deferred) {
		t.Fatalf("first finalization error = %v, want cleanup deferral", firstErr)
	}
	requestPath := credentialCleanupRequestPath(stateDir)
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(requestPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := finalizeAcceptedEnrollment(stateDir, "edge-1", "", "", false, "token-1"); err != nil {
		t.Fatalf("repeated accepted config seed re-finalized credentials: %v", err)
	}
	info, err := os.Stat(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Fatalf("repeated accepted config seed reset pending age: got %v, want %v", info.ModTime(), old)
	}
	if err := finalizeAcceptedEnrollment(stateDir, "edge-1", "", "", false, "token-2"); !errors.As(err, &deferred) {
		t.Fatalf("new credential generation was mistaken for completed generation: %v", err)
	}
}

func TestFinalizeAcceptedEnrollmentWithoutSubmittedCredentialDoesNotRequestCleanup(t *testing.T) {
	stateDir := t.TempDir()
	if err := finalizeAcceptedEnrollment(stateDir, "edge-1", "", "", false, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(enrollmentReceiptPath(stateDir, "edge-1")); err != nil {
		t.Fatalf("accepted enrollment receipt: %v", err)
	}
	if _, err := os.Stat(credentialCleanupRequestPath(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("credential-free reconnect requested cleanup: %v", err)
	}
}

func TestFinalizeAcceptedRotationCompletesBeforeCredentialScrub(t *testing.T) {
	stateDir := t.TempDir()
	storageDir := t.TempDir()
	tokenFile := t.TempDir() + "/enroll.env"
	runtimeFile := t.TempDir() + "/edge.env"
	if _, _, err := nodeidentity.LoadOrCreatePrivateKey(stateDir, "edge-1", storageDir, true, "rotation-token"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenFile, []byte("EDGE_ENROLLMENT_TOKEN=rotation-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFile, []byte("HELMSMAN_ROTATE_NODE_IDENTITY=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := finalizeAcceptedEnrollment(stateDir, "edge-1", tokenFile, runtimeFile, true, "token-1"); err != nil {
		t.Fatal(err)
	}
	if _, status, err := nodeidentity.LoadOrCreatePrivateKey(stateDir, "edge-1", storageDir, false, ""); err != nil || status != nodeidentity.LoadStatusLoaded {
		t.Fatalf("completed identity status = %q, err=%v", status, err)
	}
	completed, err := os.ReadFile(credentialCleanupCompletionPath(stateDir))
	if err != nil {
		t.Fatalf("native direct cleanup did not persist completion: %v", err)
	}
	if completedAt, parseErr := strconv.ParseInt(strings.TrimSpace(string(completed)), 10, 64); parseErr != nil || completedAt <= 0 {
		t.Fatalf("native direct cleanup completion = %q, err %v", completed, parseErr)
	}
	if got := testutil.ToFloat64(CredentialCleanupLastSuccessTimestamp); got <= 0 {
		t.Fatalf("direct cleanup success timestamp was not exposed: %v", got)
	}
}

func TestNativeDirectCleanupRemovesObsoleteRequest(t *testing.T) {
	stateDir := t.TempDir()
	if err := markEnrollmentAccepted(stateDir, "edge-old"); err != nil {
		t.Fatal(err)
	}
	if err := requestCredentialCleanup(stateDir, "edge-old"); err != nil {
		t.Fatal(err)
	}
	tokenFile := filepath.Join(t.TempDir(), "enroll.env")
	runtimeFile := filepath.Join(t.TempDir(), "edge.env")
	if err := os.WriteFile(tokenFile, []byte("EDGE_ENROLLMENT_TOKEN=new-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFile, []byte("HELMSMAN_ROTATE_NODE_IDENTITY=\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := finalizeAcceptedEnrollment(stateDir, "edge-new", tokenFile, runtimeFile, false, "token-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(credentialCleanupRequestPath(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("obsolete native cleanup request remained: %v", err)
	}
}

func TestFinalizeAcceptedRotationRetainsCredentialsWhenCompletionFails(t *testing.T) {
	tokenFile := t.TempDir() + "/enroll.env"
	runtimeFile := t.TempDir() + "/edge.env"
	token := []byte("EDGE_ENROLLMENT_TOKEN=rotation-token\n")
	runtime := []byte("HELMSMAN_ROTATE_NODE_IDENTITY=true\n")
	if err := os.WriteFile(tokenFile, token, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFile, runtime, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := finalizeAcceptedEnrollment(t.TempDir(), "edge-1", tokenFile, runtimeFile, true, "token-1"); err == nil {
		t.Fatal("expected rotation completion failure")
	}
	gotToken, _ := os.ReadFile(tokenFile)
	gotRuntime, _ := os.ReadFile(runtimeFile)
	if string(gotToken) != string(token) || string(gotRuntime) != string(runtime) {
		t.Fatal("credentials changed before durable rotation completion")
	}
}

func TestCredentialCleanupRequestPreservesFilesUntilWorkerRuns(t *testing.T) {
	stateDir := t.TempDir()
	tokenFile := t.TempDir() + "/enroll.env"
	runtimeFile := t.TempDir() + "/edge.env"
	token := []byte("  export EDGE_ENROLLMENT_TOKEN=one-time-secret\n")
	runtime := []byte("NODE_ID=edge-1\n\texport HELMSMAN_ROTATE_NODE_IDENTITY=true\n")
	if err := os.WriteFile(tokenFile, token, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFile, runtime, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := markEnrollmentAccepted(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	if err := requestCredentialCleanup(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	gotToken, _ := os.ReadFile(tokenFile)
	gotRuntime, _ := os.ReadFile(runtimeFile)
	if string(gotToken) != string(token) || string(gotRuntime) != string(runtime) {
		t.Fatal("deferred cleanup modified credential files in the non-root enrollment path")
	}
	if _, err := os.Stat(credentialCleanupRequestPath(stateDir)); err != nil {
		t.Fatalf("cleanup request: %v", err)
	}
	if err := processCredentialCleanupRequest(stateDir, "edge-1", tokenFile, runtimeFile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(credentialCleanupRequestPath(stateDir)); !os.IsNotExist(err) {
		t.Fatalf("cleanup request remained after successful scrub: %v", err)
	}
	completed, err := os.ReadFile(credentialCleanupCompletionPath(stateDir))
	if err != nil {
		t.Fatalf("durable cleanup completion: %v", err)
	}
	completedAt, err := strconv.ParseInt(strings.TrimSpace(string(completed)), 10, 64)
	if err != nil || completedAt <= 0 {
		t.Fatalf("cleanup completion timestamp = %q, err %v", completed, err)
	}
	gotToken, _ = os.ReadFile(tokenFile)
	gotRuntime, _ = os.ReadFile(runtimeFile)
	if strings.Contains(string(gotToken), "one-time-secret") || strings.Contains(string(gotRuntime), "true") {
		t.Fatalf("credentials were not scrubbed: token=%q runtime=%q", gotToken, gotRuntime)
	}
	if len(gotToken) != len(token) || len(gotRuntime) != len(runtime) {
		t.Fatal("credential scrub changed file length")
	}
}

func TestCredentialCleanupObserverRestoresRootHelperCompletionMetric(t *testing.T) {
	stateDir := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(credentialCleanupCompletionPath(stateDir)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := recordCredentialCleanupCompletion(stateDir); err != nil {
		t.Fatal(err)
	}
	CredentialCleanupLastSuccessTimestamp.Set(0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ObserveCredentialCleanup(ctx, stateDir, "/native/enroll.env", "/native/edge.env")
	if got := testutil.ToFloat64(CredentialCleanupLastSuccessTimestamp); got <= 0 {
		t.Fatalf("root-helper completion timestamp was not exposed: %v", got)
	}
}

func TestContainerCleanupCompletionRequiresRootOwnership(t *testing.T) {
	if !usesRootCredentialCleanup(containerTokenFile, containerRuntimeEnvFile) {
		t.Fatal("fixed container credential paths did not select the root-helper trust model")
	}
	if usesRootCredentialCleanup("/native/enroll.env", "/native/edge.env") {
		t.Fatal("native credential paths selected the root-helper trust model")
	}
	if trustedCredentialCleanupCompletionOwner(true, 1001) {
		t.Fatal("Helmsman uid can forge the root-helper completion")
	}
	if !trustedCredentialCleanupCompletionOwner(true, 0) {
		t.Fatal("root-helper completion was rejected")
	}
}

func TestCredentialCleanupObserverFailsClosedOnUnreadableState(t *testing.T) {
	stateDir := t.TempDir()
	enrollmentDir := filepath.Dir(credentialCleanupCompletionPath(stateDir))
	if err := os.MkdirAll(enrollmentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-target", credentialCleanupCompletionPath(stateDir)); err != nil {
		t.Fatal(err)
	}
	CredentialCleanupLastSuccessTimestamp.Set(float64(time.Now().Unix()))
	CredentialCleanupPending.Set(0)
	CredentialCleanupPendingSeconds.Set(42)
	CredentialCleanupObservationError.Set(0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ObserveCredentialCleanup(ctx, stateDir, "/native/enroll.env", "/native/edge.env")
	if got := testutil.ToFloat64(CredentialCleanupLastSuccessTimestamp); got != 0 {
		t.Fatalf("unreadable state retained stale success timestamp: %v", got)
	}
	if got := testutil.ToFloat64(CredentialCleanupPending); got != 1 {
		t.Fatalf("unreadable state pending gauge = %v, want 1", got)
	}
	if got := testutil.ToFloat64(CredentialCleanupObservationError); got != 1 {
		t.Fatalf("unreadable state observation-error gauge = %v, want 1", got)
	}
}

func TestCredentialCleanupObservationErrorPreservesKnownPendingAge(t *testing.T) {
	stateDir := t.TempDir()
	if err := markEnrollmentAccepted(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	if err := requestCredentialCleanup(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	oldestPending := now.Add(-10 * time.Minute)
	requestPath := credentialCleanupRequestPath(stateDir)
	if err := os.Chtimes(requestPath, oldestPending, oldestPending); err != nil {
		t.Fatal(err)
	}
	state := credentialCleanupObservationState{}
	observeCredentialCleanupOnce(stateDir, false, now, &state)
	if got := testutil.ToFloat64(CredentialCleanupPendingSeconds); got < 599 {
		t.Fatalf("initial pending age = %v, want about 600 seconds", got)
	}
	if err := os.Remove(requestPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing-request", requestPath); err != nil {
		t.Fatal(err)
	}
	observeCredentialCleanupOnce(stateDir, false, now.Add(2*time.Minute), &state)
	if got := testutil.ToFloat64(CredentialCleanupPendingSeconds); got < 719 {
		t.Fatalf("observation failure reset known pending age: %v", got)
	}
	if got := testutil.ToFloat64(CredentialCleanupObservationError); got != 1 {
		t.Fatalf("observation-error gauge = %v, want 1", got)
	}
}

func TestCredentialCleanupMissingDirectoryPreservesKnownPendingAge(t *testing.T) {
	parent := t.TempDir()
	stateDir := filepath.Join(parent, "state")
	if err := markEnrollmentAccepted(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	if err := requestCredentialCleanup(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	oldestPending := now.Add(-10 * time.Minute)
	requestPath := credentialCleanupRequestPath(stateDir)
	if err := os.Chtimes(requestPath, oldestPending, oldestPending); err != nil {
		t.Fatal(err)
	}
	state := credentialCleanupObservationState{}
	observeCredentialCleanupOnce(stateDir, false, now, &state)
	if err := os.Rename(stateDir, stateDir+".unmounted"); err != nil {
		t.Fatal(err)
	}
	observeCredentialCleanupOnce(stateDir, false, now.Add(2*time.Minute), &state)
	if got := testutil.ToFloat64(CredentialCleanupPendingSeconds); got < 719 {
		t.Fatalf("missing state directory reset known pending age: %v", got)
	}
	if got := testutil.ToFloat64(CredentialCleanupObservationError); got != 1 {
		t.Fatalf("missing state observation-error gauge = %v, want 1", got)
	}
}

func TestCredentialCleanupObserverPrefersPendingRequestOverCompletion(t *testing.T) {
	stateDir := t.TempDir()
	if err := markEnrollmentAccepted(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	if err := requestCredentialCleanup(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	if err := recordCredentialCleanupCompletion(stateDir); err != nil {
		t.Fatal(err)
	}
	CredentialCleanupLastSuccessTimestamp.Set(float64(time.Now().Unix()))
	CredentialCleanupPending.Set(0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ObserveCredentialCleanup(ctx, stateDir, "/native/enroll.env", "/native/edge.env")
	if got := testutil.ToFloat64(CredentialCleanupLastSuccessTimestamp); got != 0 {
		t.Fatalf("pending request retained stale completion timestamp: %v", got)
	}
	if got := testutil.ToFloat64(CredentialCleanupPending); got != 1 {
		t.Fatalf("pending gauge = %v, want 1", got)
	}
}

func TestDeferredCredentialCleanupRequestSurvivesScrubFailure(t *testing.T) {
	stateDir := t.TempDir()
	runtimeFile := t.TempDir() + "/edge.env"
	if err := os.WriteFile(runtimeFile, []byte("HELMSMAN_ROTATE_NODE_IDENTITY=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := markEnrollmentAccepted(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	if err := requestCredentialCleanup(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	if err := processCredentialCleanupRequest(stateDir, "edge-1", t.TempDir()+"/missing.env", runtimeFile); err == nil {
		t.Fatal("expected missing credential file to fail cleanup")
	}
	if _, err := os.Stat(credentialCleanupRequestPath(stateDir)); err != nil {
		t.Fatalf("failed cleanup lost retry request: %v", err)
	}
}

func TestCredentialCleanupRequestRequiresAcceptedReceipt(t *testing.T) {
	stateDir := t.TempDir()
	tokenFile := t.TempDir() + "/enroll.env"
	runtimeFile := t.TempDir() + "/edge.env"
	if err := os.WriteFile(tokenFile, []byte("EDGE_ENROLLMENT_TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFile, []byte("HELMSMAN_ROTATE_NODE_IDENTITY=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := requestCredentialCleanup(stateDir, "edge-1"); err == nil {
		t.Fatal("cleanup request without an accepted enrollment receipt succeeded")
	}
	data, err := os.ReadFile(tokenFile)
	if err != nil || !strings.Contains(string(data), "secret") {
		t.Fatalf("credential changed before acceptance: %q, err %v", data, err)
	}
}

func TestCredentialCleanupRequestIsInsertOnce(t *testing.T) {
	stateDir := t.TempDir()
	if err := markEnrollmentAccepted(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	if err := requestCredentialCleanup(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	requestPath := credentialCleanupRequestPath(stateDir)
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(requestPath, old, old); err != nil {
		t.Fatal(err)
	}
	if err := requestCredentialCleanup(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(requestPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Fatalf("repeated cleanup request reset pending age: got %v, want %v", info.ModTime(), old)
	}
}

func TestCredentialCleanupRequestDoesNotReplaceDifferentReceipt(t *testing.T) {
	stateDir := t.TempDir()
	if err := markEnrollmentAccepted(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	if err := requestCredentialCleanup(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	if err := requestCredentialCleanup(stateDir, "edge-2"); err == nil {
		t.Fatal("cleanup request replaced a different node receipt")
	}
	contents, err := os.ReadFile(credentialCleanupRequestPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Base(enrollmentReceiptPath(stateDir, "edge-1"))
	if strings.TrimSpace(string(contents)) != want {
		t.Fatalf("cleanup request = %q, want receipt %q", contents, want)
	}
}

func TestCredentialCleanupRequestReplacesOlderAcceptedGeneration(t *testing.T) {
	stateDir := t.TempDir()
	if err := markEnrollmentAccepted(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	if err := requestCredentialCleanup(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	if err := markEnrollmentAccepted(stateDir, "edge-2"); err != nil {
		t.Fatal(err)
	}
	if err := requestCredentialCleanup(stateDir, "edge-2"); err != nil {
		t.Fatalf("new accepted enrollment could not replace stale cleanup request: %v", err)
	}
	contents, err := os.ReadFile(credentialCleanupRequestPath(stateDir))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Base(enrollmentReceiptPath(stateDir, "edge-2"))
	if strings.TrimSpace(string(contents)) != want {
		t.Fatalf("replacement request = %q, want %q", contents, want)
	}
}

func TestCredentialCleanupRequestRejectsDifferentNodeReceipt(t *testing.T) {
	stateDir := t.TempDir()
	tokenFile := t.TempDir() + "/enroll.env"
	runtimeFile := t.TempDir() + "/edge.env"
	if err := os.WriteFile(tokenFile, []byte("EDGE_ENROLLMENT_TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFile, []byte("HELMSMAN_ROTATE_NODE_IDENTITY=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := markEnrollmentAccepted(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	if err := requestCredentialCleanup(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	if err := processCredentialCleanupRequest(stateDir, "edge-2", tokenFile, runtimeFile); err == nil {
		t.Fatal("cleanup request for a different node receipt succeeded")
	}
	data, err := os.ReadFile(tokenFile)
	if err != nil || !strings.Contains(string(data), "secret") {
		t.Fatalf("credential changed for mismatched node receipt: %q, err %v", data, err)
	}
}

func TestRewriteEnvValueRejectsSymlink(t *testing.T) {
	target := t.TempDir() + "/target.env"
	link := t.TempDir() + "/link.env"
	if err := os.WriteFile(target, []byte("EDGE_ENROLLMENT_TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := rewriteEnvValue(link, "EDGE_ENROLLMENT_TOKEN", ""); err == nil {
		t.Fatal("symlink credential target was accepted")
	}
}

func TestCredentialCleanupRequestRejectsSymlink(t *testing.T) {
	stateDir := t.TempDir()
	tokenFile := t.TempDir() + "/enroll.env"
	runtimeFile := t.TempDir() + "/edge.env"
	if err := os.WriteFile(tokenFile, []byte("EDGE_ENROLLMENT_TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFile, []byte("HELMSMAN_ROTATE_NODE_IDENTITY=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := markEnrollmentAccepted(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir() + "/request"
	if err := os.WriteFile(target, []byte(filepath.Base(enrollmentReceiptPath(stateDir, "edge-1"))), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, credentialCleanupRequestPath(stateDir)); err != nil {
		t.Fatal(err)
	}
	if err := processCredentialCleanupRequest(stateDir, "edge-1", tokenFile, runtimeFile); err == nil {
		t.Fatal("symlink cleanup request was accepted")
	}
	data, err := os.ReadFile(tokenFile)
	if err != nil || !strings.Contains(string(data), "secret") {
		t.Fatalf("credential changed after symlink request: %q, err %v", data, err)
	}
}

func TestEnrollmentReceiptWriteRejectsSymlink(t *testing.T) {
	stateDir := t.TempDir()
	dirFD, err := openOrCreateEnrollmentDirectoryNoFollow(stateDir)
	if err != nil {
		t.Fatal(err)
	}
	_ = unix.Close(dirFD)
	target := filepath.Join(t.TempDir(), "target")
	if writeErr := os.WriteFile(target, []byte("unchanged\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if symlinkErr := os.Symlink(target, enrollmentReceiptPath(stateDir, "edge-1")); symlinkErr != nil {
		t.Fatal(symlinkErr)
	}
	if markErr := markEnrollmentAccepted(stateDir, "edge-1"); markErr == nil {
		t.Fatal("symlinked enrollment receipt was accepted")
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "unchanged\n" {
		t.Fatalf("receipt target changed: %q, err %v", contents, err)
	}
}

func TestEnrollmentStateWriteRejectsSymlinkedAncestor(t *testing.T) {
	realParent := t.TempDir()
	aliasParent := filepath.Join(t.TempDir(), "state-link")
	if err := os.Symlink(realParent, aliasParent); err != nil {
		t.Fatal(err)
	}
	if err := markEnrollmentAccepted(filepath.Join(aliasParent, "state"), "edge-1"); err == nil {
		t.Fatal("symlinked state-directory ancestor was accepted")
	}
}

func TestCredentialCleanupWorkerRejectsUntrustedStateOwner(t *testing.T) {
	stateDir := t.TempDir()
	tokenFile := filepath.Join(t.TempDir(), "enroll.env")
	runtimeFile := filepath.Join(t.TempDir(), "edge.env")
	if err := os.WriteFile(tokenFile, []byte("EDGE_ENROLLMENT_TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFile, []byte("HELMSMAN_ROTATE_NODE_IDENTITY=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := markEnrollmentAccepted(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	if err := requestCredentialCleanup(stateDir, "edge-1"); err != nil {
		t.Fatal(err)
	}
	untrustedUID := uint32(os.Geteuid()) + 1
	if err := processCredentialCleanupRequest(stateDir, "edge-1", tokenFile, runtimeFile, untrustedUID); err == nil {
		t.Fatal("root helper accepted request and receipt from an untrusted owner")
	}
	contents, err := os.ReadFile(tokenFile)
	if err != nil || !strings.Contains(string(contents), "secret") {
		t.Fatalf("untrusted state scrubbed credential: %q, err %v", contents, err)
	}
}

func TestCredentialCleanupWorkerRejectsSymlinkedEnrollmentDirectory(t *testing.T) {
	stateDir := t.TempDir()
	targetDir := t.TempDir()
	tokenFile := t.TempDir() + "/enroll.env"
	runtimeFile := t.TempDir() + "/edge.env"
	if err := os.WriteFile(tokenFile, []byte("EDGE_ENROLLMENT_TOKEN=secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFile, []byte("HELMSMAN_ROTATE_NODE_IDENTITY=true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	receiptName := filepath.Base(enrollmentReceiptPath(stateDir, "edge-1"))
	if err := os.WriteFile(filepath.Join(targetDir, receiptName), []byte("accepted\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetDir, "credential-cleanup.request"), []byte(receiptName+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(targetDir, filepath.Join(stateDir, "enrollment")); err != nil {
		t.Fatal(err)
	}

	if err := processCredentialCleanupRequest(stateDir, "edge-1", tokenFile, runtimeFile); err == nil {
		t.Fatal("symlinked enrollment directory was accepted by the root helper")
	}
	token, err := os.ReadFile(tokenFile)
	if err != nil || !strings.Contains(string(token), "secret") {
		t.Fatalf("credential changed through symlinked parent: %q, err %v", token, err)
	}
}

func TestCredentialCleanupWorkerValidation(t *testing.T) {
	if err := validateCredentialCleanupWorker(1001, containerStateDir, "node-1", containerTokenFile, containerRuntimeEnvFile); err == nil {
		t.Fatal("non-root worker accepted")
	}
	if err := validateCredentialCleanupWorker(0, "", "node-1", containerTokenFile, containerRuntimeEnvFile); err == nil {
		t.Fatal("worker with missing state accepted")
	}
	if err := validateCredentialCleanupWorker(0, "/state", "node-1", containerTokenFile, containerRuntimeEnvFile); err == nil {
		t.Fatal("worker accepted a redirected state directory")
	}
	if err := validateCredentialCleanupWorker(0, containerStateDir, "node-1", "/tmp/token", containerRuntimeEnvFile); err == nil {
		t.Fatal("worker accepted a redirected credential file")
	}
	if err := validateCredentialCleanupWorker(0, containerStateDir, "node-1", containerTokenFile, containerRuntimeEnvFile); err != nil {
		t.Fatalf("valid worker rejected: %v", err)
	}
}

func TestCredentialCleanupMissingStateFailsClosedAfterColdStart(t *testing.T) {
	missingState := filepath.Join(t.TempDir(), "not-created")
	if err := processCredentialCleanupRequest(missingState, "edge-1", "/unused/token", "/unused/runtime"); err != nil {
		t.Fatalf("missing enrollment state should be idle: %v", err)
	}

	CredentialCleanupPending.Set(1)
	CredentialCleanupPendingSeconds.Set(42)
	CredentialCleanupLastSuccessTimestamp.Set(float64(time.Now().Unix()))
	CredentialCleanupObservationError.Set(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ObserveCredentialCleanup(ctx, missingState, "/native/enroll.env", "/native/edge.env")
	if got := testutil.ToFloat64(CredentialCleanupPending); got != 1 {
		t.Fatalf("pending gauge = %v, want 1", got)
	}
	if got := testutil.ToFloat64(CredentialCleanupPendingSeconds); got < 0 {
		t.Fatalf("pending age = %v, want non-negative", got)
	}
	if got := testutil.ToFloat64(CredentialCleanupLastSuccessTimestamp); got != 0 {
		t.Fatalf("missing state retained stale success timestamp: %v", got)
	}
	if got := testutil.ToFloat64(CredentialCleanupObservationError); got != 1 {
		t.Fatalf("missing state observation error = %v, want 1", got)
	}
}

func TestContainerCredentialCleanupMissingStateFailsClosedAfterRestart(t *testing.T) {
	missingState := filepath.Join(t.TempDir(), "not-created")
	state := credentialCleanupObservationState{}
	CredentialCleanupPending.Set(0)
	CredentialCleanupPendingSeconds.Set(0)
	CredentialCleanupObservationError.Set(0)
	observeCredentialCleanupOnce(missingState, true, time.Now(), &state)
	if got := testutil.ToFloat64(CredentialCleanupPending); got != 1 {
		t.Fatalf("container missing-state pending gauge = %v, want 1", got)
	}
	if got := testutil.ToFloat64(CredentialCleanupObservationError); got != 1 {
		t.Fatalf("container missing-state observation error = %v, want 1", got)
	}
}

func TestRewriteEnvValueRejectsReplacementWiderThanExistingField(t *testing.T) {
	path := t.TempDir() + "/edge.env"
	original := []byte("KEY=x\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rewriteEnvValue(path, "KEY", "too-wide"); err == nil {
		t.Fatal("expected width validation error")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("failed rewrite changed file: %q", got)
	}
}

func TestRewriteEnvValueScrubsEveryDuplicateDefinition(t *testing.T) {
	path := t.TempDir() + "/edge.env"
	original := []byte("EDGE_ENROLLMENT_TOKEN=first-secret\nexport EDGE_ENROLLMENT_TOKEN=second-secret\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rewriteEnvValue(path, "EDGE_ENROLLMENT_TOKEN", ""); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "first-secret") || strings.Contains(string(got), "second-secret") {
		t.Fatalf("duplicate credential definition survived scrub: %q", got)
	}
	if len(got) != len(original) {
		t.Fatal("credential scrub changed file length")
	}
}
