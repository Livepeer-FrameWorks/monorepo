package balancer

import (
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// freshLBWithWeights resets the shared state manager and pins scoring weights,
// returning a load balancer ready to score. rateNodeWithReason reads weights via
// state.DefaultManager().GetWeights(), so the manager must exist and carry known
// weights for the scoring-path assertions to be deterministic.
func freshLBWithWeights(t *testing.T, cpu, ram, bw, geo, bonus uint64) *LoadBalancer {
	t.Helper()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetWeights(cpu, ram, bw, geo, bonus)
	return NewLoadBalancer(logging.NewLoggerWithService("test"))
}

// goodSnap is a node snapshot that passes every admission gate and scores > 0:
// valid RAM/BW limits, bandwidth available, pre-computed cpu/ram scores. Tests
// mutate a copy to drive a single gate to its rejection.
func goodSnap() state.EnhancedBalancerNodeSnapshot {
	return state.EnhancedBalancerNodeSnapshot{
		Host:        "n1",
		NodeID:      "n1",
		IsActive:    true,
		RAMMax:      1024,
		BWLimit:     1000,
		BWAvailable: 500,
		CPUScore:    300,
		RAMScore:    400,
	}
}

// rateNodeWithReason is the Go port of MistServer's C++ rate(): it admits or
// rejects a node and, if admitted, composes a score. Each rejection reason is a
// distinct routing outcome, so we pin which one fires for each failure mode —
// the reason string is what surfaces to operators as the "why no node" message.
func TestRateNodeWithReason_Rejections(t *testing.T) {
	lb := freshLBWithWeights(t, 500, 500, 1000, 1000, 50)

	t.Run("host invalid: zero RAMMax", func(t *testing.T) {
		s := goodSnap()
		s.RAMMax = 0
		if score, reason := lb.rateNodeWithReason(s, "", 0, 0, nil, false); score != 0 || reason != rejectHostInvalid {
			t.Fatalf("got (%d, %q), want (0, %q)", score, reason, rejectHostInvalid)
		}
	})

	t.Run("host invalid: zero BWLimit", func(t *testing.T) {
		s := goodSnap()
		s.BWLimit = 0
		// BWLimit==0 must reject BEFORE the score math divides by BWLimit.
		if score, reason := lb.rateNodeWithReason(s, "", 0, 0, nil, false); score != 0 || reason != rejectHostInvalid {
			t.Fatalf("got (%d, %q), want (0, %q)", score, reason, rejectHostInvalid)
		}
	})

	t.Run("bandwidth exhausted: BWAvailable zero", func(t *testing.T) {
		s := goodSnap()
		s.BWAvailable = 0
		if score, reason := lb.rateNodeWithReason(s, "", 0, 0, nil, false); score != 0 || reason != rejectBandwidthExhaust {
			t.Fatalf("got (%d, %q), want (0, %q)", score, reason, rejectBandwidthExhaust)
		}
	})

	t.Run("stream missing on node", func(t *testing.T) {
		s := goodSnap() // no Streams map
		if score, reason := lb.rateNodeWithReason(s, "live+x", 0, 0, nil, true); score != 0 || reason != rejectStreamMissing {
			t.Fatalf("got (%d, %q), want (0, %q)", score, reason, rejectStreamMissing)
		}
	})

	t.Run("stream present but no inputs", func(t *testing.T) {
		s := goodSnap()
		s.Streams = map[string]state.BalancerStreamSummary{"live+x": {Inputs: 0}}
		if score, reason := lb.rateNodeWithReason(s, "live+x", 0, 0, nil, true); score != 0 || reason != rejectStreamNoInputs {
			t.Fatalf("got (%d, %q), want (0, %q)", score, reason, rejectStreamNoInputs)
		}
	})

	t.Run("replicated stream excluded for source selection only", func(t *testing.T) {
		s := goodSnap()
		s.GeoLatitude, s.GeoLongitude = 52, 5
		s.Streams = map[string]state.BalancerStreamSummary{"live+x": {Inputs: 1, Replicated: true}}

		// Source selection: a replicated node cannot serve as the pull origin.
		if score, reason := lb.rateNodeWithReason(s, "live+x", 52, 5, nil, true); score != 0 || reason != rejectStreamReplicated {
			t.Fatalf("source selection: got (%d, %q), want (0, %q)", score, reason, rejectStreamReplicated)
		}
		// Viewer selection (not source): a replicated edge IS a valid place to watch.
		if score, reason := lb.rateNodeWithReason(s, "live+x", 52, 5, nil, false); score == 0 || reason != "" {
			t.Fatalf("viewer selection: got (%d, %q), want (>0, \"\")", score, reason)
		}
	})

	t.Run("config streams: not allowed by node config", func(t *testing.T) {
		s := goodSnap()
		// Stream must be resident (else stream-missing fires first); the config
		// list is what then rejects it.
		s.Streams = map[string]state.BalancerStreamSummary{"live+x": {Inputs: 1}}
		s.ConfigStreams = []string{"other"}
		if score, reason := lb.rateNodeWithReason(s, "live+x", 0, 0, nil, false); score != 0 || reason != rejectConfigStreams {
			t.Fatalf("got (%d, %q), want (0, %q)", score, reason, rejectConfigStreams)
		}
	})

	t.Run("config streams: exact and wildcard allow paths", func(t *testing.T) {
		// config "live" admits "live+foo" (the '+' wildcard) and "live bar"
		// (the space form); config "live+foo" admits itself exactly.
		cases := []struct{ config, stream string }{
			{"live+foo", "live+foo"}, // exact
			{"live", "live+foo"},     // confStream+"+" prefix
			{"live", "live bar"},     // confStream+" " prefix
		}
		for _, c := range cases {
			s := goodSnap()
			s.Streams = map[string]state.BalancerStreamSummary{c.stream: {Inputs: 1}}
			s.ConfigStreams = []string{c.config}
			if score, reason := lb.rateNodeWithReason(s, c.stream, 0, 0, nil, false); score == 0 || reason != "" {
				t.Fatalf("config=%q stream=%q: got (%d, %q), want admitted", c.config, c.stream, score, reason)
			}
		}
	})
}

// The score is the sum of five independent components. With identical node and
// viewer coordinates the geo distance is 0 (the dist-clamp guard makes acos(1)
// exact), so every term is fully determined: cpu(300) + ram(400) + bw(1000) +
// geo(1000) + bonus(50) = 2750. Pinning the exact total guards the formula
// against silent reweighting or a dropped term.
func TestRateNodeWithReason_ScoreComposition(t *testing.T) {
	lb := freshLBWithWeights(t, 500, 500, 1000, 1000, 50)
	s := goodSnap()
	s.GeoLatitude, s.GeoLongitude = 52, 5
	s.Streams = map[string]state.BalancerStreamSummary{"live+x": {Inputs: 1}}

	score, reason := lb.rateNodeWithReason(s, "live+x", 52, 5, nil, false)
	if reason != "" {
		t.Fatalf("unexpected rejection: %q", reason)
	}
	if score != 2750 {
		t.Fatalf("composed score = %d, want 2750 (300+400+1000+1000+50)", score)
	}

	// The stream bonus is exactly the delta between a resident-stream request
	// and an anonymous (no-stream) request on the same node.
	noStream, _ := lb.rateNodeWithReason(s, "", 52, 5, nil, false)
	if score-noStream != 50 {
		t.Fatalf("stream bonus delta = %d, want 50", score-noStream)
	}
}

// Tag adjustments shift the score by a signed amount. A negative adjustment that
// exceeds the base score floors it to 0 and yields rejectAdjustedToZero (the
// node is effectively tagged out); a positive adjustment raises the score.
func TestRateNodeWithReason_TagAdjustment(t *testing.T) {
	// Minimal positive base: only bw contributes (cpu/ram/geo/bonus weighted 0,
	// CPUScore/RAMScore zeroed). bwScore = weights["bw"] = 10.
	lb := freshLBWithWeights(t, 0, 0, 10, 0, 0)
	s := goodSnap()
	s.CPUScore, s.RAMScore = 0, 0
	s.Tags = []string{"premium"}

	// base score (no adjustment) is 10.
	if base, reason := lb.rateNodeWithReason(s, "", 0, 0, nil, false); base != 10 || reason != "" {
		t.Fatalf("base got (%d, %q), want (10, \"\")", base, reason)
	}

	// Negative adjustment larger than the base floors to 0 → rejected.
	if score, reason := lb.rateNodeWithReason(s, "", 0, 0, map[string]int{"premium": -100}, false); score != 0 || reason != rejectAdjustedToZero {
		t.Fatalf("over-penalty got (%d, %q), want (0, %q)", score, reason, rejectAdjustedToZero)
	}

	// Positive adjustment raises the score above base.
	if score, reason := lb.rateNodeWithReason(s, "", 0, 0, map[string]int{"premium": 5}, false); score != 15 || reason != "" {
		t.Fatalf("bonus got (%d, %q), want (15, \"\")", score, reason)
	}
}
