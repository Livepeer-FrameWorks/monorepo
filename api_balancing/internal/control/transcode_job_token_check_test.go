package control

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// Every token rejection names the check that failed, so an invalid_token at the
// gateway's auth webhook is diagnosable from the verifier's log and metric.
func TestVerifyTranscodeJobTokenNamesFailedCheck(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	claims := TranscodeJobClaims{
		ManifestID: "processing+artifact", JobID: "job-1", AttemptOrGeneration: "0", Session: "job-1",
		NodeID: "edge-1", ClusterID: "media-us", TenantID: "tenant-1", SpecDigest: "abc",
		AllowedGatewayClusterIDs: []string{"media-us"}, IssuedAt: now.Unix(),
	}
	valid, err := MintTranscodeJobToken("secret", claims)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	var withUnknown map[string]any
	if unmarshalErr := json.Unmarshal(payload, &withUnknown); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	withUnknown["region"] = "us"
	unknownPayload, _ := json.Marshal(withUnknown)
	missingTenant := claims
	missingTenant.TenantID = ""
	missingPayload, _ := json.Marshal(missingTenant)
	future := claims
	future.IssuedAt = now.Add(6 * time.Minute).Unix()
	futureToken, err := MintTranscodeJobToken("secret", future)
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name, secret, token, check string
		sentinel                   error
	}{
		{name: "no verifier secret", secret: " ", token: valid, check: TranscodeTokenCheckMissing, sentinel: ErrTranscodeJobTokenMissing},
		{name: "no token", secret: "secret", token: "", check: TranscodeTokenCheckMissing, sentinel: ErrTranscodeJobTokenMissing},
		{name: "not a v1 token", secret: "secret", token: "attacker-controlled", check: TranscodeTokenCheckFormat, sentinel: ErrTranscodeJobTokenInvalid},
		{name: "other cell secret", secret: "other-secret", token: valid, check: TranscodeTokenCheckHMAC, sentinel: ErrTranscodeJobTokenInvalid},
		{name: "unknown claim", secret: "secret", token: signedTranscodePayload("secret", unknownPayload), check: TranscodeTokenCheckUnknownField, sentinel: ErrTranscodeJobTokenInvalid},
		{name: "missing tenant claim", secret: "secret", token: signedTranscodePayload("secret", missingPayload), check: TranscodeTokenCheckMissingClaim, sentinel: ErrTranscodeJobTokenInvalid},
		{name: "issued far in the future", secret: "secret", token: futureToken, check: TranscodeTokenCheckIssuedAtSkew, sentinel: ErrTranscodeJobTokenInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := VerifyTranscodeJobToken(tc.secret, tc.token, now)
			if got := TranscodeJobTokenCheck(err); got != tc.check {
				t.Fatalf("check = %q (err %v), want %q", got, err, tc.check)
			}
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("error %v does not match sentinel %v", err, tc.sentinel)
			}
		})
	}
}

// Minting and verifying Foghorns run on different hosts; ordinary drift ahead
// of the verifier must not reject a token.
func TestVerifyTranscodeJobTokenToleratesClockDrift(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	token, err := MintTranscodeJobToken("secret", TranscodeJobClaims{
		ManifestID: "live+stream", AttemptOrGeneration: "state:1", Session: "state:1",
		NodeID: "edge-1", ClusterID: "media-us", TenantID: "tenant-1", SpecDigest: "abc",
		AllowedGatewayClusterIDs: []string{"media-us"}, IssuedAt: now.Add(4 * time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyTranscodeJobToken("secret", token, now); err != nil {
		t.Fatalf("token issued 4 minutes ahead rejected: %v", err)
	}
}

func TestProcessingFailureReasonClassifiesHelmsmanErrors(t *testing.T) {
	for errMsg, want := range map[string]string{
		"source unavailable: 404 Not Found":                 "MEDIA_FAILURE_REASON_SOURCE_UNAVAILABLE",
		"no processing progress within the watchdog window": "MEDIA_FAILURE_REASON_TIMED_OUT",
		"processing stalled at 12%":                         "MEDIA_FAILURE_REASON_TIMED_OUT",
		"absolute timeout exceeded (4h)":                    "MEDIA_FAILURE_REASON_TIMED_OUT",
		"max retries exceeded":                              "MEDIA_FAILURE_REASON_TIMED_OUT",
		"AV process failed: encoder crashed":                "MEDIA_FAILURE_REASON_PROCESSING_FAILED",
		"recording validation failed: zero bytes written":   "MEDIA_FAILURE_REASON_PROCESSING_FAILED",
	} {
		if got := ProcessingFailureReason(errMsg).String(); got != want {
			t.Errorf("ProcessingFailureReason(%q) = %s, want %s", errMsg, got, want)
		}
	}
}

func TestProcessingFailureIgnoreReason(t *testing.T) {
	for _, tc := range []struct {
		name, status, assigned, reporting, want string
	}{
		{name: "assigned node fails its active job", status: "processing", assigned: "edge-1", reporting: "edge-1", want: ""},
		{name: "dispatched job", status: "dispatched", assigned: "edge-1", reporting: "edge-1", want: ""},
		{name: "already terminal", status: "failed", assigned: "edge-1", reporting: "edge-1", want: processingFailureIgnoredInactiveJob},
		{name: "requeued after stale recovery", status: "queued", assigned: "", reporting: "edge-1", want: processingFailureIgnoredInactiveJob},
		{name: "unbound assignment", status: "processing", assigned: "", reporting: "edge-1", want: processingFailureIgnoredUnassignedJob},
		{name: "foreign node", status: "processing", assigned: "edge-1", reporting: "edge-2", want: processingFailureIgnoredNodeMismatch},
		{name: "no reporting identity", status: "processing", assigned: "edge-1", reporting: "", want: processingFailureIgnoredNodeMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := processingFailureIgnoreReason(tc.status, tc.assigned, tc.reporting); got != tc.want {
				t.Fatalf("processingFailureIgnoreReason = %q, want %q", got, tc.want)
			}
		})
	}
}
