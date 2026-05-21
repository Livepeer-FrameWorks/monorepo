package orchestrator

import (
	"fmt"
	"strings"
	"testing"
)

func mkTask(host string) *Task {
	return &Task{
		Name:      host,
		ServiceID: "svc",
		Host:      host,
		Phase:     PhaseApplications,
	}
}

func mkInputs(hosts []string, region string, role string) []RolloutInput {
	out := make([]RolloutInput, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, RolloutInput{Task: mkTask(h), Region: region, Role: role})
	}
	return out
}

// waveHosts returns the host names per wave, joined for compact assertions.
func waveHosts(plan RolloutPlan) []string {
	out := make([]string, 0, len(plan.Waves))
	for _, w := range plan.Waves {
		names := make([]string, 0, len(w.Tasks))
		for _, t := range w.Tasks {
			names = append(names, t.Host)
		}
		out = append(out, strings.Join(names, ","))
	}
	return out
}

func assertWaves(t *testing.T, plan RolloutPlan, want []string) {
	t.Helper()
	got := waveHosts(plan)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("waves mismatch:\n  want %v\n  got  %v", want, got)
	}
}

// 3-broker Kafka with the default strategy (MaxUnavailable=1, no stagger,
// no canary): three sequential single-task waves. This is the canonical
// "never break quorum" pattern.
func TestBuildWaves_KafkaThreeBrokerSingleAtATime(t *testing.T) {
	inputs := mkInputs([]string{"regional-eu-1", "regional-eu-2", "regional-eu-3"}, "eu", "")
	plan := BuildWaves("kafka", inputs, UpdateStrategy{MaxUnavailable: 1})
	assertWaves(t, plan, []string{"regional-eu-1", "regional-eu-2", "regional-eu-3"})
}

// 6-host stateless Bridge across two regions: with region_stagger and
// max_unavailable=1+canary=1, EU rolls fully before US starts and each
// region runs one host at a time.
func TestBuildWaves_BridgeRegionStaggerOneAtATime(t *testing.T) {
	inputs := append(
		mkInputs([]string{"regional-eu-1", "regional-eu-2", "regional-eu-3"}, "eu", ""),
		mkInputs([]string{"regional-us-1", "regional-us-2", "regional-us-3"}, "us", "")...,
	)
	plan := BuildWaves("bridge", inputs, UpdateStrategy{MaxUnavailable: 1, Canary: 1, RegionStagger: true})
	assertWaves(t, plan, []string{
		// EU: canary, then remainder one-at-a-time.
		"regional-eu-1", "regional-eu-2", "regional-eu-3",
		// US: never overlaps with EU.
		"regional-us-1", "regional-us-2", "regional-us-3",
	})
}

// Same Bridge layout but with max_unavailable=2 to verify the chunking
// math when canary is set. Expected: canary=1 then chunks of 2.
func TestBuildWaves_CanaryThenMaxUnavailable(t *testing.T) {
	inputs := mkInputs([]string{"a", "b", "c", "d", "e"}, "eu", "")
	plan := BuildWaves("svc", inputs, UpdateStrategy{MaxUnavailable: 2, Canary: 1})
	assertWaves(t, plan, []string{
		"a",   // canary
		"b,c", // chunk
		"d,e", // chunk
	})
}

// Region stagger with multiple regions emits region blocks in stable
// alphabetical order — important for deterministic output and operator
// review.
func TestBuildWaves_RegionStaggerStableOrder(t *testing.T) {
	inputs := []RolloutInput{
		{Task: mkTask("us-1"), Region: "us"},
		{Task: mkTask("eu-1"), Region: "eu"},
		{Task: mkTask("us-2"), Region: "us"},
		{Task: mkTask("eu-2"), Region: "eu"},
		{Task: mkTask("ap-1"), Region: "ap"},
	}
	plan := BuildWaves("svc", inputs, UpdateStrategy{MaxUnavailable: 1, RegionStagger: true})
	assertWaves(t, plan, []string{"ap-1", "eu-1", "eu-2", "us-1", "us-2"})
}

// Redis-style primary_last: replicas first, primary last. Sentinel failover
// remains Redis' own availability mechanism; the rollout planner only owns
// ordering.
func TestBuildWaves_RedisPrimaryLast(t *testing.T) {
	inputs := []RolloutInput{
		{Task: mkTask("regional-eu-2"), Role: "replica"},
		{Task: mkTask("regional-eu-1"), Role: "primary"},
		{Task: mkTask("regional-eu-3"), Role: "replica"},
	}
	plan := BuildWaves("redis", inputs, UpdateStrategy{MaxUnavailable: 1, PrimaryLast: true})
	assertWaves(t, plan, []string{
		"regional-eu-2",
		"regional-eu-3",
		"regional-eu-1", // primary last
	})
}

// Even with PrimaryLast set, when no input has Role=="primary" the planner
// must not synthesize a phantom primary wave. All inputs flow through
// the non-primary path.
func TestBuildWaves_PrimaryLastNoPrimaryPresent(t *testing.T) {
	inputs := mkInputs([]string{"a", "b", "c"}, "eu", "replica")
	plan := BuildWaves("redis", inputs, UpdateStrategy{MaxUnavailable: 1, PrimaryLast: true})
	assertWaves(t, plan, []string{"a", "b", "c"})
}

// Singleton: one host, one wave, regardless of strategy values.
func TestBuildWaves_Singleton(t *testing.T) {
	inputs := mkInputs([]string{"central-eu-1"}, "eu", "")
	plan := BuildWaves("commodore", inputs, UpdateStrategy{MaxUnavailable: 1, RegionStagger: true})
	assertWaves(t, plan, []string{"central-eu-1"})
}

// Empty inputs → empty plan; never an empty Wave.
func TestBuildWaves_Empty(t *testing.T) {
	plan := BuildWaves("svc", nil, UpdateStrategy{MaxUnavailable: 1})
	if len(plan.Waves) != 0 {
		t.Fatalf("expected zero waves, got %d", len(plan.Waves))
	}
}

// MaxUnavailable=0 is treated as 1 (the safe default) — a missing
// strategy can't accidentally roll the whole tier at once.
func TestBuildWaves_ZeroMaxUnavailableIsOne(t *testing.T) {
	inputs := mkInputs([]string{"a", "b", "c"}, "eu", "")
	plan := BuildWaves("svc", inputs, UpdateStrategy{})
	assertWaves(t, plan, []string{"a", "b", "c"})
}

// Canary larger than the input set → one wave with everything, no second
// wave. Defensive against misconfigured strategies.
func TestBuildWaves_CanaryLargerThanInputs(t *testing.T) {
	inputs := mkInputs([]string{"a", "b"}, "eu", "")
	plan := BuildWaves("svc", inputs, UpdateStrategy{MaxUnavailable: 1, Canary: 5})
	assertWaves(t, plan, []string{"a,b"})
}

// Region stagger + primary_last together: primary always last across all
// regions, never inside a region block. Covers a Redis-EU + Redis-US
// world if that ever lands.
func TestBuildWaves_PrimaryLastWithRegionStagger(t *testing.T) {
	inputs := []RolloutInput{
		{Task: mkTask("eu-2"), Region: "eu", Role: "replica"},
		{Task: mkTask("eu-1"), Region: "eu", Role: "primary"},
		{Task: mkTask("us-2"), Region: "us", Role: "replica"},
		{Task: mkTask("us-1"), Region: "us", Role: "primary"},
	}
	plan := BuildWaves("redis", inputs, UpdateStrategy{
		MaxUnavailable: 1, PrimaryLast: true, RegionStagger: true,
	})
	assertWaves(t, plan, []string{
		// Replicas roll first, region-staggered.
		"eu-2",
		"us-2",
		// Then primaries last, also region-staggered.
		"eu-1",
		"us-1",
	})
}

// DefaultStrategyFor returns the registry entry for known services and
// a safe fallback for unknowns.
func TestDefaultStrategyFor(t *testing.T) {
	cases := []struct {
		svc  string
		want UpdateStrategy
	}{
		{"foghorn", UpdateStrategy{MaxUnavailable: 1, Canary: 1, RegionStagger: true}},
		{"kafka", UpdateStrategy{MaxUnavailable: 1}},
		{"kafka-controller", UpdateStrategy{MaxUnavailable: 1}},
		{"redis", UpdateStrategy{MaxUnavailable: 1, PrimaryLast: true}},
		{"bridge", UpdateStrategy{MaxUnavailable: 1, Canary: 1, RegionStagger: true}},
		{"commodore", UpdateStrategy{MaxUnavailable: 1}},
		{"this-service-does-not-exist", UpdateStrategy{MaxUnavailable: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.svc, func(t *testing.T) {
			got := DefaultStrategyFor(tc.svc)
			if got != tc.want {
				t.Fatalf("DefaultStrategyFor(%q) = %+v, want %+v", tc.svc, got, tc.want)
			}
		})
	}
}
