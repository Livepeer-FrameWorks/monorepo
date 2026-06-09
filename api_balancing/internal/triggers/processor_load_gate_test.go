package triggers

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// TestApplyLoadGate pins the billing-aware admission policy: paying tenants are
// never load-shed, free tenants over their allowance shed at the over-allowance
// threshold, and at the redline ALL free traffic is shed regardless of
// allowance state. This is the load-defence contract — a regression here either
// rejects paying customers or never sheds free load under pressure.
func TestApplyLoadGate(t *testing.T) {
	thresh := loadThresholds{rejectOverAllowance: 0.5, rejectAnyFree: 0.95}
	cases := []struct {
		name       string
		loadFrac   float64
		isFreeTier bool
		exhausted  bool
		want       admissionDecision
	}{
		{"paying admitted at idle", 0.0, false, false, admissionAdmit},
		{"paying admitted at redline", 0.99, false, true, admissionAdmit},
		{"free within allowance low load", 0.20, true, false, admissionAdmit},
		{"free within allowance just under redline", 0.94, true, false, admissionAdmit},
		{"free within allowance at redline rejected", 0.95, true, false, admissionRejectRedline},
		{"free exhausted below over-allowance admitted", 0.49, true, true, admissionAdmit},
		{"free exhausted at over-allowance rejected", 0.50, true, true, admissionRejectOverAllowance},
		{"free exhausted at redline rejected as redline", 0.95, true, true, admissionRejectRedline},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := applyLoadGate(c.loadFrac, c.isFreeTier, c.exhausted, thresh)
			if got != c.want {
				t.Errorf("applyLoadGate(%v, free=%v, exhausted=%v) = %v, want %v",
					c.loadFrac, c.isFreeTier, c.exhausted, got, c.want)
			}
		})
	}
}

// TestEnvFloatInRange verifies the env override parser used by the load
// thresholds. Out-of-range and unparseable values must fall back so a fat-finger
// FOGHORN_INGEST_REJECT_*_LOAD can never produce a nonsensical gate (e.g. a
// negative or >1 fraction).
func TestEnvFloatInRange(t *testing.T) {
	const key = "FOGHORN_TEST_ENV_FLOAT"
	cases := []struct {
		name string
		set  bool
		raw  string
		want float64
	}{
		{"unset uses fallback", false, "", 0.5},
		{"empty uses fallback", true, "   ", 0.5},
		{"unparseable uses fallback", true, "abc", 0.5},
		{"at-min boundary rejected", true, "0", 0.5},
		{"below-min rejected", true, "-0.2", 0.5},
		{"at-max boundary rejected", true, "1", 0.5},
		{"above-max rejected", true, "1.5", 0.5},
		{"valid in range", true, "0.7", 0.7},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.set {
				t.Setenv(key, c.raw)
			}
			got := envFloatInRange(key, 0.5, 0, 1)
			if got != c.want {
				t.Errorf("envFloatInRange(%q=%q) = %v, want %v", key, c.raw, got, c.want)
			}
		})
	}
}

// TestLoadThresholdDefaults pins the documented default policy points so a
// silent default change is caught: ingest sheds over-allowance free at 50% and
// all free at the 95% redline; viewers (cheaper but multiply faster) shed
// over-allowance at 80% and all free at 95%.
func TestLoadThresholdDefaults(t *testing.T) {
	ingest := ingestLoadThresholds()
	if ingest.rejectOverAllowance != 0.5 || ingest.rejectAnyFree != 0.95 {
		t.Errorf("ingest defaults = %+v, want {0.5, 0.95}", ingest)
	}
	viewer := viewerLoadThresholds()
	if viewer.rejectOverAllowance != 0.8 || viewer.rejectAnyFree != 0.95 {
		t.Errorf("viewer defaults = %+v, want {0.8, 0.95}", viewer)
	}
}

// TestClusterLoadFraction verifies the percent→fraction conversion and the
// fail-safe contract: an empty cluster ID or a cluster with no health samples
// returns ok=false so callers treat "no signal" as "don't gate", never as
// "fully loaded".
func TestClusterLoadFraction(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()

	t.Run("empty cluster id", func(t *testing.T) {
		if frac, ok := clusterLoadFraction("  "); ok || frac != 0 {
			t.Errorf("clusterLoadFraction(empty) = (%v, %v), want (0, false)", frac, ok)
		}
	})

	t.Run("no samples", func(t *testing.T) {
		if frac, ok := clusterLoadFraction("ghost-cluster"); ok || frac != 0 {
			t.Errorf("clusterLoadFraction(no nodes) = (%v, %v), want (0, false)", frac, ok)
		}
	})

	t.Run("valid signal converts to fraction", func(t *testing.T) {
		sm.SetNodeInfo("n1", "", true, nil, nil, "", "", nil)
		sm.SetNodeConnectionInfo(context.Background(), "n1", "", "", "media-a", nil)
		sm.UpdateNodeMetrics("n1", struct {
			CPU                  float64
			RAMMax               float64
			RAMCurrent           float64
			UpSpeed              float64
			DownSpeed            float64
			BWLimit              float64
			CapIngest            bool
			CapEdge              bool
			CapStorage           bool
			CapProcessing        bool
			Roles                []string
			StorageCapacityBytes uint64
			StorageUsedBytes     uint64
			ProcessingClasses    map[string]state.ClassCapacity
		}{CPU: 60})
		// Clear the new-node staleness so ClusterLoad counts it.
		sm.TouchNode("n1", true)

		frac, ok := clusterLoadFraction("media-a")
		if !ok || frac != 0.6 {
			t.Errorf("clusterLoadFraction(seeded 60%% CPU) = (%v, %v), want (0.6, true)", frac, ok)
		}
	})
}

// TestShouldSurfaceDecklogError pins which trigger types propagate a Decklog
// send failure to the caller (so Mist retries) versus those whose telemetry is
// best-effort and silently dropped. A wrong classification either spams retries
// on fire-and-forget triggers or loses lifecycle-critical events.
func TestShouldSurfaceDecklogError(t *testing.T) {
	surface := []string{
		string(mist.TriggerUserEnd),
		string(mist.TriggerStreamEnd),
		string(mist.TriggerPushEnd),
		string(mist.TriggerRecordingEnd),
		string(mist.TriggerRecordingSegment),
		string(mist.TriggerLivepeerSegmentComplete),
		string(mist.TriggerProcessAVSegmentComplete),
	}
	for _, tt := range surface {
		trig := &ipcpb.MistTrigger{TriggerType: tt}
		if !shouldSurfaceDecklogError(trig) {
			t.Errorf("shouldSurfaceDecklogError(%q) = false, want true", tt)
		}
	}
	for _, tt := range []string{string(mist.TriggerPushRewrite), string(mist.TriggerUserNew), "UNKNOWN", ""} {
		trig := &ipcpb.MistTrigger{TriggerType: tt}
		if shouldSurfaceDecklogError(trig) {
			t.Errorf("shouldSurfaceDecklogError(%q) = true, want false", tt)
		}
	}
}

// TestLivepeerGatewayURLFromInstance pins the PHYSICAL-ONLY contract: an
// instance is addressable for broadcaster failover only by its public instance
// host. A nil instance or one without that metadata key returns "" and is
// skipped — falling back to a pooled name would silently defeat per-instance
// failover.
func TestLivepeerGatewayURLFromInstance(t *testing.T) {
	if got := livepeerGatewayURLFromInstance(nil); got != "" {
		t.Errorf("nil instance = %q, want empty", got)
	}
	if got := livepeerGatewayURLFromInstance(&quartermasterpb.ServiceInstance{}); got != "" {
		t.Errorf("no metadata = %q, want empty", got)
	}
	pooled := &quartermasterpb.ServiceInstance{Metadata: map[string]string{"other": "x"}}
	if got := livepeerGatewayURLFromInstance(pooled); got != "" {
		t.Errorf("missing physical host key = %q, want empty", got)
	}
	physical := &quartermasterpb.ServiceInstance{
		Metadata: map[string]string{servicedefs.LivepeerGatewayMetadataPublicInstanceHost: "gw1.example.com"},
	}
	if got := livepeerGatewayURLFromInstance(physical); got != "https://gw1.example.com" {
		t.Errorf("physical host = %q, want https://gw1.example.com", got)
	}
}

// TestLivepeerVODEnvOverrides verifies the operator-retune env knobs clamp to
// the compiled defaults on unset/invalid/non-positive input, so a bad env can
// never zero out the per-segment deadline or min-speed.
func TestLivepeerVODEnvOverrides(t *testing.T) {
	t.Run("deadline default when unset", func(t *testing.T) {
		if got := livepeerVODDeadlineMs(); got != mist.LivepeerVODSegmentDeadlineMs {
			t.Errorf("deadline unset = %d, want default %d", got, mist.LivepeerVODSegmentDeadlineMs)
		}
	})
	t.Run("deadline invalid falls back", func(t *testing.T) {
		t.Setenv("LIVEPEER_VOD_DEADLINE_MS", "nope")
		if got := livepeerVODDeadlineMs(); got != mist.LivepeerVODSegmentDeadlineMs {
			t.Errorf("deadline invalid = %d, want default %d", got, mist.LivepeerVODSegmentDeadlineMs)
		}
	})
	t.Run("deadline non-positive falls back", func(t *testing.T) {
		t.Setenv("LIVEPEER_VOD_DEADLINE_MS", "0")
		if got := livepeerVODDeadlineMs(); got != mist.LivepeerVODSegmentDeadlineMs {
			t.Errorf("deadline 0 = %d, want default %d", got, mist.LivepeerVODSegmentDeadlineMs)
		}
	})
	t.Run("deadline valid override", func(t *testing.T) {
		t.Setenv("LIVEPEER_VOD_DEADLINE_MS", "12345")
		if got := livepeerVODDeadlineMs(); got != 12345 {
			t.Errorf("deadline override = %d, want 12345", got)
		}
	})
	t.Run("min speed default when unset", func(t *testing.T) {
		if got := livepeerVODMinSpeed(); got != mist.LivepeerVODMinSpeed {
			t.Errorf("min speed unset = %v, want default %v", got, mist.LivepeerVODMinSpeed)
		}
	})
	t.Run("min speed invalid falls back", func(t *testing.T) {
		t.Setenv("LIVEPEER_VOD_MIN_SPEED", "x")
		if got := livepeerVODMinSpeed(); got != mist.LivepeerVODMinSpeed {
			t.Errorf("min speed invalid = %v, want default %v", got, mist.LivepeerVODMinSpeed)
		}
	})
	t.Run("min speed valid override", func(t *testing.T) {
		t.Setenv("LIVEPEER_VOD_MIN_SPEED", "0.25")
		if got := livepeerVODMinSpeed(); got != 0.25 {
			t.Errorf("min speed override = %v, want 0.25", got)
		}
	})
}
