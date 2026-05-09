// Package dvrpolicy resolves the per-session DVR live-window policy that
// applies to a tenant's recording on a given cluster.
//
// Two distinct concepts must not be confused:
//
//   - Live DVR window:  how far back live viewers can seek while the stream is
//     still recording. Bounded; rolling. Backed by Mist's targetAge + maxEntries.
//
//   - Retention until: post-end artifact expiry timestamp. Independent from
//     the live window; it never clamps Mist targetAge or maxEntries.
//
// The resolver is pure: same inputs always produce the same Effective output.
package dvrpolicy

// PlatformAbsoluteMaxSeconds is the hardest cap the platform allows for a live
// DVR window across any tier or cluster. Anything past this would either blow
// past sane HLS playlist sizes or imply we are running an archive product, not
// a live DVR product.
const PlatformAbsoluteMaxSeconds = 72 * 3600 // 3 days

// Tier is the billing-tier-level DVR policy. Sourced from purser tier
// entitlements via shared.DVRPolicy on the wire.
type Tier struct {
	// Default applied when the caller does not request a window.
	DefaultWindowSeconds int
	// Hardest window allowed without explicit cluster opt-in.
	MaxWindowSeconds int
	// Mist split (segment duration). Auto-scales by tier so manifests stay
	// bounded: 6s for short windows, 12s for ~1d, 24s for multi-day.
	DefaultSegmentDurationSeconds int
	// Hardest manifest entry count allowed for this tier.
	MaxEntries int
	// True when the cluster may extend MaxWindowSeconds for this tenant
	// (Enterprise opt-in path). False tiers ignore cluster MaxWindowSeconds.
	AllowClusterExtension bool
}

// Cluster is the per-cluster ceiling that operators may set in cluster.yaml.
// Zero values mean "no cluster cap" and the tier's caps stand.
type Cluster struct {
	MaxWindowSeconds int
	MaxEntries       int
}

// Request is what the caller asked for at startDVR / record:true time.
type Request struct {
	// 0 means "use the tier default".
	DVRWindowSeconds int
}

// Effective is the resolved configuration applied to a single DVR session.
// Sidecar receives these verbatim and feeds them straight into the Mist push
// URL — no further interpretation, no fallbacks.
type Effective struct {
	DVRWindowSeconds       int
	SegmentDurationSeconds int
	MaxEntries             int
	UsedDefaultFallback    bool
}

// Resolve clamps the requested window through every applicable bound and
// returns the effective configuration.
func Resolve(req Request, tier Tier, cluster Cluster) Effective {
	requested := req.DVRWindowSeconds
	if requested <= 0 {
		requested = tier.DefaultWindowSeconds
	}

	tierMax := tier.MaxWindowSeconds
	if tier.AllowClusterExtension && cluster.MaxWindowSeconds > tierMax {
		tierMax = cluster.MaxWindowSeconds
	}

	// Fallback for a fully misconfigured tier (no defaults, no max). A live
	// recording has to pick a window; emit 1h so something sensible runs
	// while the misconfiguration is investigated. The platform safety cap
	// further down still applies.
	usedDefaultFallback := false
	if requested <= 0 && tierMax <= 0 {
		requested = 3600
		usedDefaultFallback = true
	}

	caps := []int{PlatformAbsoluteMaxSeconds}
	if requested > 0 {
		caps = append(caps, requested)
	}
	if tierMax > 0 {
		caps = append(caps, tierMax)
	}
	if cluster.MaxWindowSeconds > 0 {
		caps = append(caps, cluster.MaxWindowSeconds)
	}
	window := minPositive(caps)

	seg := segmentDurationFor(window, tier)
	maxEntries := ceilDiv(window, seg)
	if tier.MaxEntries > 0 && maxEntries > tier.MaxEntries {
		maxEntries = tier.MaxEntries
	}
	if cluster.MaxEntries > 0 && maxEntries > cluster.MaxEntries {
		maxEntries = cluster.MaxEntries
	}

	// If max_entries clipped the window, shrink the window to match so Mist
	// targetAge and maxEntries describe the same horizon.
	if cap := maxEntries * seg; cap < window {
		window = cap
	}

	return Effective{
		DVRWindowSeconds:       window,
		SegmentDurationSeconds: seg,
		MaxEntries:             maxEntries,
		UsedDefaultFallback:    usedDefaultFallback,
	}
}

// segmentDurationFor picks the tier's preferred segment duration for the
// resolved window. Tiers with longer ceilings inherently use longer segments,
// but a short session on an Enterprise tier still gets short segments —
// what matters for live experience is segment length, not ceiling.
func segmentDurationFor(windowSeconds int, tier Tier) int {
	tierDefault := tier.DefaultSegmentDurationSeconds
	if tierDefault <= 0 {
		tierDefault = 6
	}
	switch {
	case windowSeconds <= 12*3600:
		// ≤12h fits 7,200 entries at 6s; keep snappy live experience.
		if tierDefault > 6 {
			return tierDefault
		}
		return 6
	case windowSeconds <= 24*3600:
		// ≤1d: 12s lands at 7,200 entries.
		if tierDefault > 12 {
			return tierDefault
		}
		return 12
	default:
		// >1d: 24s lands at 10,800 for 3d.
		if tierDefault > 24 {
			return tierDefault
		}
		return 24
	}
}

func minPositive(values []int) int {
	out := 0
	for _, v := range values {
		if v <= 0 {
			continue
		}
		if out == 0 || v < out {
			out = v
		}
	}
	return out
}

func ceilDiv(a, b int) int {
	if b <= 0 {
		return 0
	}
	if a%b == 0 {
		return a / b
	}
	return a/b + 1
}

// DefaultTiers returns the canonical FrameWorks tier presets. Production seeds
// these into purser.tier_entitlements; tests use them as fixtures.
func DefaultTiers() map[string]Tier {
	return map[string]Tier{
		"free": {
			DefaultWindowSeconds:          30 * 60,
			MaxWindowSeconds:              60 * 60,
			DefaultSegmentDurationSeconds: 6,
			MaxEntries:                    600,
			AllowClusterExtension:         false,
		},
		"supporter": {
			DefaultWindowSeconds:          2 * 3600,
			MaxWindowSeconds:              6 * 3600,
			DefaultSegmentDurationSeconds: 6,
			MaxEntries:                    3600,
			AllowClusterExtension:         false,
		},
		"developer": {
			DefaultWindowSeconds:          4 * 3600,
			MaxWindowSeconds:              12 * 3600,
			DefaultSegmentDurationSeconds: 6,
			MaxEntries:                    7200,
			AllowClusterExtension:         false,
		},
		"production": {
			DefaultWindowSeconds:          4 * 3600,
			MaxWindowSeconds:              24 * 3600,
			DefaultSegmentDurationSeconds: 12,
			MaxEntries:                    7200,
			AllowClusterExtension:         false,
		},
		"enterprise": {
			DefaultWindowSeconds:          4 * 3600,
			MaxWindowSeconds:              24 * 3600, // requires cluster opt-in to reach 72h
			DefaultSegmentDurationSeconds: 24,
			MaxEntries:                    10800,
			AllowClusterExtension:         true,
		},
	}
}
