package triggers

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pullsource"
)

// jwtDenyReason maps each sentinel verification error to a stable metric/label
// string. The mapping is the contract — these strings land in Prometheus labels
// and structured logs, so a silent rename would break dashboards. Asserting the
// full table pins every branch and guarantees the fall-through default is the
// only path for an unrecognised error.
func TestJWTDenyReason(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{auth.ErrTokenNotJWS, "jwt-not-a-jws"},
		{auth.ErrMissingKid, "jwt-missing-kid"},
		{auth.ErrUnknownKid, "jwt-unknown-kid"},
		{auth.ErrWrongAlgorithm, "jwt-wrong-alg"},
		{auth.ErrSignatureFailed, "jwt-sig-fail"},
		{auth.ErrMissingExpiration, "jwt-missing-exp"},
		{auth.ErrTokenExpired, "jwt-expired"},
		{auth.ErrTokenNotYetValid, "jwt-not-yet-valid"},
		{auth.ErrAudienceMismatch, "jwt-aud-mismatch"},
		{auth.ErrRequiredClaimMiss, "jwt-claim-mismatch"},
		{auth.ErrInvalidPublicKey, "jwt-bad-public-key"},
		{errors.New("some other failure"), "jwt-verify-error"},
	}
	for _, c := range cases {
		if got := jwtDenyReason(c.err); got != c.want {
			t.Errorf("jwtDenyReason(%v) = %q, want %q", c.err, got, c.want)
		}
	}

	// errors.Is must match wrapped sentinels too — denial reasons surface from
	// deep in the verifier, where the sentinel is typically wrapped with %w.
	if got := jwtDenyReason(fmt.Errorf("verify failed: %w", auth.ErrTokenExpired)); got != "jwt-expired" {
		t.Errorf("wrapped ErrTokenExpired = %q, want jwt-expired", got)
	}
}

// livepeerVODDeadlineMs / livepeerVODMinSpeed are env-overridable getters. The
// contract: a valid positive override wins; anything else (unset, empty,
// non-numeric, zero, negative) falls back to the compiled default. These guard
// against an operator typo silently zeroing the gateway budget.
func TestLivepeerVODDeadlineMs(t *testing.T) {
	t.Run("unset falls back to default", func(t *testing.T) {
		t.Setenv("LIVEPEER_VOD_DEADLINE_MS", "")
		if got := livepeerVODDeadlineMs(); got != mist.LivepeerVODSegmentDeadlineMs {
			t.Fatalf("got %d, want default %d", got, mist.LivepeerVODSegmentDeadlineMs)
		}
	})
	t.Run("valid override wins", func(t *testing.T) {
		t.Setenv("LIVEPEER_VOD_DEADLINE_MS", "12345")
		if got := livepeerVODDeadlineMs(); got != 12345 {
			t.Fatalf("got %d, want 12345", got)
		}
	})
	for _, bad := range []string{"abc", "0", "-5"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			t.Setenv("LIVEPEER_VOD_DEADLINE_MS", bad)
			if got := livepeerVODDeadlineMs(); got != mist.LivepeerVODSegmentDeadlineMs {
				t.Fatalf("override %q should fall back, got %d", bad, got)
			}
		})
	}
}

func TestLivepeerVODMinSpeed(t *testing.T) {
	t.Run("unset falls back to default", func(t *testing.T) {
		t.Setenv("LIVEPEER_VOD_MIN_SPEED", "")
		if got := livepeerVODMinSpeed(); got != mist.LivepeerVODMinSpeed {
			t.Fatalf("got %v, want default %v", got, mist.LivepeerVODMinSpeed)
		}
	})
	t.Run("valid override wins", func(t *testing.T) {
		t.Setenv("LIVEPEER_VOD_MIN_SPEED", "1.25")
		if got := livepeerVODMinSpeed(); got != 1.25 {
			t.Fatalf("got %v, want 1.25", got)
		}
	})
	for _, bad := range []string{"fast", "0", "-1.0"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			t.Setenv("LIVEPEER_VOD_MIN_SPEED", bad)
			if got := livepeerVODMinSpeed(); got != mist.LivepeerVODMinSpeed {
				t.Fatalf("override %q should fall back, got %v", bad, got)
			}
		})
	}
}

// isRelaySafeFormat gates whether an upload wrapper can be opened by Mist over
// HTTP. The split is load-bearing: unsafe wrappers (avi/flv/m4v) must stage to
// disk instead, so a false positive here spins a job that can never open its
// source. Verify the safe allowlist, the known-unsafe set, and that
// normalization (case + leading dot) does not leak an unsafe format through.
func TestIsRelaySafeFormat(t *testing.T) {
	safe := []string{".mp4", "mp4", ".MOV", "mkv", ".webm", "ts", ".m2ts", "m3u8", ".m3u"}
	for _, ext := range safe {
		if !isRelaySafeFormat(ext) {
			t.Errorf("isRelaySafeFormat(%q) = false, want true", ext)
		}
	}
	unsafe := []string{".avi", "flv", ".m4v", "AVI", "", "mp3", ".exe", "wav"}
	for _, ext := range unsafe {
		if isRelaySafeFormat(ext) {
			t.Errorf("isRelaySafeFormat(%q) = true, want false", ext)
		}
	}
}

// kindFromAssetType classifies a Commodore contentType / artifact_type into the
// relay routing kind. Only "clip" and "vod" are recognised; everything else
// (dvr, processing, unknown, empty) returns "" so the caller picks its own
// fallback rather than mis-routing. Case and surrounding whitespace are
// normalized away.
func TestKindFromAssetType(t *testing.T) {
	cases := map[string]string{
		"clip":       "clip",
		"  CLIP  ":   "clip",
		"vod":        "vod",
		"VOD":        "vod",
		"dvr":        "",
		"processing": "",
		"":           "",
		"unknown":    "",
	}
	for in, want := range cases {
		if got := kindFromAssetType(in); got != want {
			t.Errorf("kindFromAssetType(%q) = %q, want %q", in, got, want)
		}
	}
}

// formatTriggerPlacementRejects renders FilterPlacementClusters rejections into
// the single detail string persisted on a STREAM_SOURCE refusal. The empty-list
// reject uses the executing cluster as fallback context (its own ClusterID is
// unset); the per-cluster reasons carry the offending cluster. The default arm
// echoes any future reason verbatim so a new pullsource reason still surfaces.
func TestFormatTriggerPlacementRejects(t *testing.T) {
	got := formatTriggerPlacementRejects([]pullsource.PlacementReject{
		{Reason: pullsource.PlacementRejectEmptyForPrivate},
		{Reason: pullsource.PlacementRejectUnknownCluster, ClusterID: "ghost"},
		{Reason: pullsource.PlacementRejectMissingPrivateCapability, ClusterID: "edge-7"},
		{Reason: pullsource.PlacementRejectReason("future_reason"), ClusterID: "edge-9"},
	}, "exec-cluster")

	want := "cluster=exec-cluster reason=empty_for_private;" +
		"cluster=ghost reason=not_in_allowed_cluster_ids;" +
		"cluster=edge-7 reason=missing_private_capability;" +
		"cluster=edge-9 reason=future_reason"
	if got != want {
		t.Fatalf("formatTriggerPlacementRejects:\n got %q\nwant %q", got, want)
	}

	if got := formatTriggerPlacementRejects(nil, "exec"); got != "" {
		t.Fatalf("empty rejects = %q, want \"\"", got)
	}
}

// ValidatePullSourceURI is the thin trigger-layer wrapper over pullsource.IsValid.
// We don't re-test the full classifier here (pkg/pullsource owns that) — only
// that the wrapper faithfully reports valid vs blocked for representative URIs,
// so a future refactor that swaps the delegate is caught.
func TestValidatePullSourceURI(t *testing.T) {
	if !ValidatePullSourceURI("https://cdn.example.com/live/stream.m3u8") {
		t.Error("public https source should be valid")
	}
	for _, bad := range []string{"", "http://localhost/x", "no-scheme", "http://127.0.0.1/x"} {
		if ValidatePullSourceURI(bad) {
			t.Errorf("ValidatePullSourceURI(%q) = true, want false (blocked)", bad)
		}
	}
}
