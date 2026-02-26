package social

import (
	"context"
	"database/sql"
	"fmt"

	"frameworks/pkg/logging"
)

type DetectorConfig struct {
	Store     PostStore
	Collector *EventCollector
	DB        *sql.DB
	Logger    logging.Logger
}

type Detector struct {
	store     PostStore
	collector *EventCollector
	db        *sql.DB
	logger    logging.Logger
}

func NewDetector(cfg DetectorConfig) *Detector {
	return &Detector{
		store:     cfg.Store,
		collector: cfg.Collector,
		db:        cfg.DB,
		logger:    cfg.Logger,
	}
}

// DetectAll drains accumulated signals from the collector, processes each
// content type, and returns scored signals sorted by score descending.
// All data arrives via callbacks from the heartbeat agent (platform/federation)
// and crawler (knowledge) — the detector does zero polling.
func (d *Detector) DetectAll(ctx context.Context) []EventSignal {
	if d.collector == nil {
		return nil
	}

	events := d.collector.Drain()
	if len(events) == 0 {
		return nil
	}

	var platformEvents, federationEvents, knowledgeEvents []EventSignal
	for _, e := range events {
		switch e.ContentType {
		case ContentPlatformStats:
			platformEvents = append(platformEvents, e)
		case ContentFederation:
			federationEvents = append(federationEvents, e)
		case ContentKnowledge:
			knowledgeEvents = append(knowledgeEvents, e)
		}
	}

	var signals []EventSignal
	if sig := d.processPlatformStats(ctx, platformEvents); sig != nil {
		signals = append(signals, *sig)
	}
	if sig := d.processFederation(ctx, federationEvents); sig != nil {
		signals = append(signals, *sig)
	}
	if sig := d.processKnowledge(ctx, knowledgeEvents); sig != nil {
		signals = append(signals, *sig)
	}

	// Sort by score descending (simple insertion sort, at most 3 elements)
	for i := 1; i < len(signals); i++ {
		for j := i; j > 0 && signals[j].Score > signals[j-1].Score; j-- {
			signals[j], signals[j-1] = signals[j-1], signals[j]
		}
	}
	return signals
}

// lastTriggerData finds the most recent post or baseline for a content type.
func (d *Detector) lastTriggerData(ctx context.Context, ct ContentType) map[string]any {
	recent, err := d.store.ListRecent(ctx, 20)
	if err != nil {
		return nil
	}
	for _, post := range recent {
		if post.ContentType == ct && post.TriggerData != nil {
			return post.TriggerData
		}
	}
	return nil
}

// saveBaseline persists current metrics as a baseline record so the next
// cycle has something to compare against. No email is sent for baselines.
func (d *Detector) saveBaseline(ctx context.Context, ct ContentType, data map[string]any) {
	_, err := d.store.Save(ctx, PostRecord{
		ContentType:    ct,
		TweetText:      "(baseline)",
		ContextSummary: "Initial baseline snapshot — no post generated",
		TriggerData:    data,
		Status:         "baseline",
	})
	if err != nil {
		d.logger.WithError(err).WithField("content_type", string(ct)).Warn("Social detector: failed to save baseline")
	}
}

// processPlatformStats processes platform-wide live stats pushed by the
// infra_monitor's GetNetworkLiveStats callback. Multiple heartbeat cycles
// may have pushed signals — we take the latest (highest viewer count) as
// the current snapshot and compare against stored baseline.
func (d *Detector) processPlatformStats(ctx context.Context, events []EventSignal) *EventSignal {
	if len(events) == 0 {
		return nil
	}

	// Take the snapshot with the most viewers (most recent peak).
	best := events[0]
	for _, e := range events[1:] {
		cv, _ := e.Data["current_viewers"].(float64)
		bestCV, _ := best.Data["current_viewers"].(float64)
		if cv > bestCV {
			best = e
		}
	}

	viewers, _ := best.Data["current_viewers"].(float64)
	streams, _ := best.Data["active_streams"].(float64)
	bandwidthBps, _ := best.Data["bandwidth_bps"].(float64)
	nodes, _ := best.Data["active_nodes"].(float64)

	data := map[string]any{
		"current_viewers": viewers,
		"active_streams":  streams,
		"bandwidth_bps":   bandwidthBps,
		"active_nodes":    nodes,
	}

	last := d.lastTriggerData(ctx, ContentPlatformStats)
	if last == nil {
		d.saveBaseline(ctx, ContentPlatformStats, data)
		d.logger.Debug("Social detector: platform stats baseline saved")
		return nil
	}

	lastViewers, _ := last["current_viewers"].(float64)
	lastBandwidth, _ := last["bandwidth_bps"].(float64)

	// New viewer record
	if lastViewers > 0 && viewers > lastViewers {
		return &EventSignal{
			ContentType: ContentPlatformStats,
			Headline:    fmt.Sprintf("New viewer record: %.0f concurrent (previous: %.0f)", viewers, lastViewers),
			Data:        data,
			Score:       0.8,
		}
	}

	// Bandwidth milestone crossings (Gbps)
	bandwidthGbps := bandwidthBps / 1e9
	lastGbps := lastBandwidth / 1e9
	milestones := []float64{1, 10, 100, 1000}
	for _, m := range milestones {
		if bandwidthGbps >= m && lastGbps < m {
			return &EventSignal{
				ContentType: ContentPlatformStats,
				Headline:    fmt.Sprintf("Bandwidth milestone: %.1f Gbps (%.0f active streams)", bandwidthGbps, streams),
				Data:        data,
				Score:       0.7,
			}
		}
	}

	// Significant viewer growth (>25% above baseline)
	if lastViewers > 0 && viewers > lastViewers*1.25 {
		return &EventSignal{
			ContentType: ContentPlatformStats,
			Headline:    fmt.Sprintf("Viewer surge: %.0f concurrent (up %.0f%% from %.0f)", viewers, (viewers-lastViewers)/lastViewers*100, lastViewers),
			Data:        data,
			Score:       0.5,
		}
	}

	return nil
}

// processFederation aggregates per-tenant federation data pushed by the
// heartbeat agent, compares against the stored baseline, and scores.
func (d *Detector) processFederation(ctx context.Context, events []EventSignal) *EventSignal {
	if len(events) == 0 {
		return nil
	}

	var totalEvents float64
	var latencySum, failureSum float64
	for _, e := range events {
		te, _ := e.Data["total_events"].(float64)
		lat, _ := e.Data["avg_latency_ms"].(float64)
		fr, _ := e.Data["failure_rate"].(float64)
		totalEvents += te
		latencySum += lat
		failureSum += fr
	}
	avgLatency := latencySum / float64(len(events))
	avgFailureRate := failureSum / float64(len(events))

	data := map[string]any{
		"total_events":   totalEvents,
		"avg_latency_ms": avgLatency,
		"failure_rate":   avgFailureRate,
		"tenant_count":   float64(len(events)),
	}

	last := d.lastTriggerData(ctx, ContentFederation)
	if last == nil {
		d.saveBaseline(ctx, ContentFederation, data)
		d.logger.Debug("Social detector: federation baseline saved")
		return nil
	}

	lastTotalEvents, _ := last["total_events"].(float64)
	lastLatency, _ := last["avg_latency_ms"].(float64)

	// Latency improvement (>20% drop)
	if lastLatency > 0 && avgLatency < lastLatency*0.8 {
		return &EventSignal{
			ContentType: ContentFederation,
			Headline:    fmt.Sprintf("Federation latency improved: %.0fms avg (down from %.0fms)", avgLatency, lastLatency),
			Data:        data,
			Score:       0.6,
		}
	}

	// Event volume milestone
	eventMilestones := []float64{100, 1000, 10000, 100000}
	for _, m := range eventMilestones {
		if totalEvents >= m && lastTotalEvents < m {
			return &EventSignal{
				ContentType: ContentFederation,
				Headline:    fmt.Sprintf("Federation milestone: %.0f+ cross-cluster events in 24h", m),
				Data:        data,
				Score:       0.6,
			}
		}
	}

	return nil
}

// processKnowledge picks the best knowledge signal, enriches it with a
// sample chunk from the database, and deduplicates against recent posts.
func (d *Detector) processKnowledge(ctx context.Context, events []EventSignal) *EventSignal {
	if len(events) == 0 {
		return nil
	}

	best := events[0]
	for _, e := range events[1:] {
		if e.Score > best.Score {
			best = e
		}
	}

	// Enrich with a sample chunk from DB if available
	if d.db != nil {
		pageURL, _ := best.Data["page_url"].(string)
		if pageURL != "" {
			var sampleChunk string
			_ = d.db.QueryRowContext(ctx, `
				SELECT chunk_text FROM skipper.skipper_knowledge
				WHERE source_url = $1
				ORDER BY chunk_index ASC
				LIMIT 1
			`, pageURL).Scan(&sampleChunk)
			if sampleChunk != "" {
				if len(sampleChunk) > 500 {
					sampleChunk = sampleChunk[:500] + "..."
				}
				best.Data["sample_content"] = sampleChunk
			}
		}
	}

	// Check if we've already posted about this page recently
	recent, _ := d.store.ListRecent(ctx, 20)
	pageURL, _ := best.Data["page_url"].(string)
	for _, post := range recent {
		if post.ContentType == ContentKnowledge && post.TriggerData != nil {
			if v, _ := post.TriggerData["page_url"].(string); v == pageURL {
				best.Score = 0.1
				break
			}
		}
	}

	if best.Score < 0.2 {
		return nil
	}

	return &best
}
