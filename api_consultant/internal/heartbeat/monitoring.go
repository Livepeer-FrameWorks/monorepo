package heartbeat

import (
	"context"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
)

// streamToggle is the per-stream Skipper monitoring override, resolved from
// Commodore's tri-state monitoring_enabled column.
type streamToggle int

const (
	toggleInherit streamToggle = iota // follow the tenant's tier entitlement
	toggleOn                          // monitor regardless of tier
	toggleOff                         // never monitor
)

func toggleFromProto(t commodorepb.MonitoringToggle) streamToggle {
	switch t {
	case commodorepb.MonitoringToggle_MONITORING_TOGGLE_ON:
		return toggleOn
	case commodorepb.MonitoringToggle_MONITORING_TOGGLE_OFF:
		return toggleOff
	default:
		return toggleInherit
	}
}

// monitored is the layered decision: a stream is monitored when the tenant-wide
// master switch is on, the stream isn't explicitly muted, and it is either an
// explicit per-stream opt-in (ON, which overrides tier) or an INHERIT stream on
// a tier-entitled tenant.
func monitored(tenantWideEnabled, tierEntitled bool, t streamToggle) bool {
	if !tenantWideEnabled {
		return false
	}
	if t == toggleOff {
		return false
	}
	return t == toggleOn || (t == toggleInherit && tierEntitled)
}

// tenantMonitoring is the per-cycle resolved monitoring state for one tenant.
// Monitored holds the candidate monitored stream UUIDs (commodore.streams.id;
// the identifier Periscope filters on). Toggle decision only — liveness is
// resolved during snapshot load via Periscope SampleCount.
type tenantMonitoring struct {
	TenantID          string
	TenantWideEnabled bool
	TierEntitled      bool
	Monitored         []string
	AllStreamCount    int
}

// eligible reports whether the tenant has any candidate monitored stream.
// Liveness is enforced later.
func (tm tenantMonitoring) eligible() bool {
	if !tm.TenantWideEnabled {
		return false
	}
	return len(tm.Monitored) > 0
}

// coversAll reports whether the monitored set covers every one of the tenant's
// streams. When true the per-stream-scoped snapshot is identical to the cheap
// tenant-wide aggregate, so loadSnapshot takes the fast path.
func (tm tenantMonitoring) coversAll() bool {
	if !tm.TenantWideEnabled {
		return false
	}
	if tm.AllStreamCount <= 0 {
		return false
	}
	return len(tm.Monitored) == tm.AllStreamCount
}

// resolveTenantMonitoring composes tier entitlement (Purser), the tenant-wide
// switch (passed in from the Quartermaster listing), and per-stream toggles
// (Commodore) into a tenantMonitoring decision.
func (a *Agent) resolveTenantMonitoring(ctx context.Context, tenantID string, tenantWideEnabled bool) tenantMonitoring {
	tm := tenantMonitoring{
		TenantID:          tenantID,
		TenantWideEnabled: tenantWideEnabled,
	}
	if !tenantWideEnabled {
		return tm
	}
	tm.TierEntitled = a.tierEntitled(ctx, tenantID)

	if a.commodore == nil {
		if a.logger != nil {
			a.logger.WithField("tenant_id", tenantID).Warn("Commodore unavailable; skipping Skipper stream monitoring")
		}
		return tm
	}
	resp, err := a.commodore.ListStreamMonitoring(ctx, tenantID)
	if err != nil {
		if a.logger != nil {
			a.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Stream monitoring lookup failed; skipping tenant")
		}
		return tm
	}

	rows := resp.GetStreams()
	tm.AllStreamCount = len(rows)
	for _, row := range rows {
		// Key on the public stream UUID: Periscope stores and filters
		// stream_health_5m.stream_id as commodore.streams.id (a UUID), not the
		// Mist internal_name (which lives in a separate column).
		streamID := row.GetStreamId()
		if streamID == "" {
			continue
		}
		if monitored(tenantWideEnabled, tm.TierEntitled, toggleFromProto(row.GetMonitoringToggle())) {
			tm.Monitored = append(tm.Monitored, streamID)
		}
	}
	return tm
}

// resolveTenant resolves a single tenant's monitoring decision, including its
// tenant-wide switch. Used by event-driven paths (Lookout) that target one
// tenant rather than sweeping all of them. Missing tenants and lookup errors are
// ineligible because the tenant-wide switch is policy input.
func (a *Agent) resolveTenant(ctx context.Context, tenantID string) tenantMonitoring {
	tenantWide := false
	if a.quartermaster == nil {
		if a.logger != nil {
			a.logger.WithField("tenant_id", tenantID).Warn("Quartermaster unavailable; skipping Skipper stream monitoring")
		}
		return a.resolveTenantMonitoring(ctx, tenantID, tenantWide)
	}
	rows, err := a.quartermaster.ListActiveTenantsWithMonitoring(ctx)
	if err != nil {
		if a.logger != nil {
			a.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Active tenant lookup failed; skipping Skipper stream monitoring")
		}
		return a.resolveTenantMonitoring(ctx, tenantID, tenantWide)
	}
	for _, r := range rows {
		if r.GetTenantId() == tenantID {
			tenantWide = r.GetMonitoringEnabled()
			break
		}
	}
	return a.resolveTenantMonitoring(ctx, tenantID, tenantWide)
}

// tierEntitled reports whether the tenant's billing tier meets the configured
// Skipper monitoring tier. When gating is disabled or Purser is unavailable,
// the tenant is treated as entitled so billing outages do not suppress health
// monitoring.
func (a *Agent) tierEntitled(ctx context.Context, tenantID string) bool {
	if a.requiredTierLevel <= 0 {
		return true
	}
	if a.purser == nil {
		if a.logger != nil {
			a.logger.WithField("tenant_id", tenantID).Warn("Purser unavailable; treating tenant as tier-entitled")
		}
		return true
	}
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, tenantID)
	status, err := a.purser.GetBillingStatus(ctx, tenantID)
	if err != nil {
		if a.logger != nil {
			a.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to fetch billing status; treating tenant as tier-entitled")
		}
		return true
	}
	tier := status.GetTier()
	if tier == nil {
		if a.logger != nil {
			a.logger.WithField("tenant_id", tenantID).Warn("Billing status missing tier; treating tenant as tier-entitled")
		}
		return true
	}
	return int(tier.TierLevel) >= a.requiredTierLevel
}

// aggregateHealthSummaries combines per-stream health summaries into one
// tenant-subset aggregate. Means are sample-weighted (lossless because
// SampleCount is carried); totals are summed. Streams with no samples are
// skipped, and AvgFps<=0 streams are excluded from the FPS mean (Mist reports
// 0 fps as "unknown"). CurrentQualityTier is carried from the stream with the
// most samples (the dominant experience) so the scoped path doesn't drop it
// from the investigation prompt; no cross-stream tier ordering is assumed.
func aggregateHealthSummaries(parts []*periscopepb.StreamHealthSummary) *periscopepb.StreamHealthSummary {
	out := &periscopepb.StreamHealthSummary{}
	var totalSamples, fpsSamples, dominantSamples int64
	var bitrateAcc, bufferAcc, fpsAcc float64
	for _, p := range parts {
		if p == nil {
			continue
		}
		out.TotalRebufferCount += p.GetTotalRebufferCount()
		out.TotalIssueCount += p.GetTotalIssueCount()
		if p.GetHasActiveIssues() {
			out.HasActiveIssues = true
		}
		sc := p.GetSampleCount()
		if sc <= 0 {
			continue
		}
		if sc > dominantSamples {
			dominantSamples = sc
			out.CurrentQualityTier = p.GetCurrentQualityTier()
		}
		totalSamples += sc
		w := float64(sc)
		bitrateAcc += p.GetAvgBitrate() * w
		bufferAcc += p.GetAvgBufferHealth() * w
		if p.GetAvgFps() > 0 {
			fpsAcc += p.GetAvgFps() * w
			fpsSamples += sc
		}
	}
	out.SampleCount = totalSamples
	if totalSamples > 0 {
		out.AvgBitrate = bitrateAcc / float64(totalSamples)
		out.AvgBufferHealth = bufferAcc / float64(totalSamples)
	}
	if fpsSamples > 0 {
		out.AvgFps = fpsAcc / float64(fpsSamples)
	}
	return out
}

// aggregateQoeSummaries combines per-stream client QoE summaries. Means are
// weighted by active sessions; sessions sum; peak packet loss is the max.
func aggregateQoeSummaries(parts []*periscopepb.ClientQoeSummary) *periscopepb.ClientQoeSummary {
	out := &periscopepb.ClientQoeSummary{}
	var totalSessions int64
	var peak, plAcc, biAcc, boAcc, ctAcc float64
	for _, p := range parts {
		if p == nil {
			continue
		}
		out.TotalActiveSessions += p.GetTotalActiveSessions()
		if p.GetPeakPacketLossRate() > peak {
			peak = p.GetPeakPacketLossRate()
		}
		s := p.GetTotalActiveSessions()
		if s <= 0 {
			continue
		}
		totalSessions += s
		w := float64(s)
		plAcc += p.GetAvgPacketLossRate() * w
		biAcc += p.GetAvgBandwidthIn() * w
		boAcc += p.GetAvgBandwidthOut() * w
		ctAcc += p.GetAvgConnectionTime() * w
	}
	out.PeakPacketLossRate = &peak
	if totalSessions > 0 {
		div := float64(totalSessions)
		avgPL := plAcc / div
		out.AvgPacketLossRate = &avgPL
		out.AvgBandwidthIn = biAcc / div
		out.AvgBandwidthOut = boAcc / div
		out.AvgConnectionTime = ctAcc / div
	}
	return out
}
