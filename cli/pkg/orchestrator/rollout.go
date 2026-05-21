package orchestrator

import (
	"sort"
)

// RolloutInput is a single host-scoped task plus the metadata BuildWaves
// uses to partition it (region label, primary/replica role). Callers
// populate Region and Role from the manifest before invoking BuildWaves;
// keeping these out of Task itself avoids polluting Task's identity model
// and keeps BuildWaves a pure data-in / data-out function.
type RolloutInput struct {
	Task   *Task
	Region string // region label from manifest host metadata; empty = unknown
	Role   string // "primary" | "replica" | "" — only consulted when PrimaryLast is set
}

// Wave is one rolling-update increment: every Task inside runs in parallel,
// and the next Wave does not start until every host in this Wave reports
// ready. Empty Wave is invalid (BuildWaves never emits one).
type Wave struct {
	Tasks []*Task
}

// RolloutPlan is the per-service result of BuildWaves: an ordered list of
// Waves to execute sequentially. BuildWaves is intentionally single-service
// so the algorithm stays simple and testable; callers compose multiple
// RolloutPlans at the command layer.
type RolloutPlan struct {
	Service string
	Waves   []Wave
}

// BuildWaves partitions a single service's RolloutInputs into rolling
// waves according to strategy. Pure function: no I/O, no host lookups —
// callers resolve region/role from the manifest first.
//
// Algorithm:
//  1. If PrimaryLast: split inputs into non-primary and primary; the primary
//     group becomes one final wave (or its own canary+max_unavailable run
//     if there are multiple primaries, which is rare).
//  2. If RegionStagger: split non-primary inputs by Region. Each region is
//     a contiguous block of waves; no wave ever mixes regions.
//  3. Within each (region) block, the first Canary inputs become wave 1;
//     the remainder chunks into waves of size max(MaxUnavailable, 1).
//  4. Stable ordering inside each wave: sort by host name. Same inputs in
//     produce the same RolloutPlan out — tests and operator review benefit.
func BuildWaves(service string, inputs []RolloutInput, strategy UpdateStrategy) RolloutPlan {
	plan := RolloutPlan{Service: service}
	if len(inputs) == 0 {
		return plan
	}

	var nonPrimary, primary []RolloutInput
	if strategy.PrimaryLast {
		for _, in := range inputs {
			if in.Role == "primary" {
				primary = append(primary, in)
			} else {
				nonPrimary = append(nonPrimary, in)
			}
		}
	} else {
		nonPrimary = append(nonPrimary, inputs...)
	}

	plan.Waves = appendWaves(plan.Waves, nonPrimary, strategy)
	if len(primary) > 0 {
		// Primary group rolls last; copy the strategy but force
		// MaxUnavailable=1 there — primaries are never rolled in parallel
		// even if the surrounding stateless tier was. RegionStagger is
		// preserved so multi-region primaries (rare) still keep regions
		// separate.
		primaryStrategy := strategy
		primaryStrategy.MaxUnavailable = 1
		primaryStrategy.Canary = 0
		plan.Waves = appendWaves(plan.Waves, primary, primaryStrategy)
	}
	return plan
}

// appendWaves builds the wave list for one role-bucket (either non-primary
// or primary). It handles region staggering and canary+max_unavailable
// chunking; the caller decides which inputs constitute the bucket.
func appendWaves(out []Wave, inputs []RolloutInput, strategy UpdateStrategy) []Wave {
	if len(inputs) == 0 {
		return out
	}

	regions := groupByRegion(inputs, strategy.RegionStagger)
	for _, region := range regions {
		sortInputsByHost(region)
		canary := strategy.Canary
		maxU := max(strategy.MaxUnavailable, 1)

		i := 0
		if canary > 0 && len(region) > 0 {
			cut := min(canary, len(region))
			out = append(out, Wave{Tasks: tasksOf(region[:cut])})
			i = cut
		}
		for i < len(region) {
			end := min(i+maxU, len(region))
			out = append(out, Wave{Tasks: tasksOf(region[i:end])})
			i = end
		}
	}
	return out
}

// groupByRegion returns inputs partitioned by region label, in stable
// alphabetical order. When staggering is off, all inputs come back as a
// single group so the rest of appendWaves doesn't branch on the flag.
func groupByRegion(inputs []RolloutInput, stagger bool) [][]RolloutInput {
	if !stagger {
		return [][]RolloutInput{append([]RolloutInput(nil), inputs...)}
	}
	byRegion := map[string][]RolloutInput{}
	for _, in := range inputs {
		byRegion[in.Region] = append(byRegion[in.Region], in)
	}
	keys := make([]string, 0, len(byRegion))
	for k := range byRegion {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([][]RolloutInput, 0, len(keys))
	for _, k := range keys {
		out = append(out, byRegion[k])
	}
	return out
}

func sortInputsByHost(in []RolloutInput) {
	sort.SliceStable(in, func(i, j int) bool {
		hi, hj := "", ""
		if in[i].Task != nil {
			hi = in[i].Task.Host
		}
		if in[j].Task != nil {
			hj = in[j].Task.Host
		}
		return hi < hj
	})
}

func tasksOf(in []RolloutInput) []*Task {
	out := make([]*Task, 0, len(in))
	for _, x := range in {
		out = append(out, x.Task)
	}
	return out
}
