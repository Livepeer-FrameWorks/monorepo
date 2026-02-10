package diagnostics

import (
	"fmt"
	"strings"
)

// CorrelationPattern identifies a known failure mode.
type CorrelationPattern string

const (
	PatternNetworkDegradation CorrelationPattern = "network_degradation"
	PatternEncoderOverload    CorrelationPattern = "encoder_overload"
	PatternViewerSideIssues   CorrelationPattern = "viewer_side_issues"
	PatternIngestInstability  CorrelationPattern = "ingest_instability"
	PatternCDNPressure        CorrelationPattern = "cdn_pressure"
)

// MetricCorrelation represents a matched failure hypothesis.
type MetricCorrelation struct {
	Pattern    CorrelationPattern
	Confidence float64
	Signals    []string
	Hypothesis string
}

type signal struct {
	metric    string
	direction string // "above" or "below"
}

type patternDef struct {
	pattern CorrelationPattern
	signals []signal
	// disambiguate: if this metric is NOT deviated, boost confidence slightly
	absenceBoost []string
}

var patterns = []patternDef{
	{
		pattern: PatternNetworkDegradation,
		signals: []signal{
			{"avg_packet_loss", "above"},
			{"avg_bandwidth_in", "below"},
			{"avg_buffer_health", "below"},
		},
	},
	{
		pattern: PatternEncoderOverload,
		signals: []signal{
			{"avg_fps", "below"},
			{"avg_bitrate", "below"},
		},
		absenceBoost: []string{"avg_packet_loss"},
	},
	{
		pattern: PatternViewerSideIssues,
		signals: []signal{
			{"avg_buffer_health", "below"},
			{"total_rebuffer_count", "above"},
		},
		absenceBoost: []string{"avg_bandwidth_out"},
	},
	{
		pattern: PatternIngestInstability,
		signals: []signal{
			{"avg_bitrate", "below"},
			{"avg_fps", "below"},
			{"total_issue_count", "above"},
		},
	},
	{
		pattern: PatternCDNPressure,
		signals: []signal{
			{"avg_bandwidth_out", "above"},
			{"active_sessions", "above"},
		},
	},
}

// Correlate matches deviation patterns to known failure hypotheses.
// Pure function — no LLM calls, no I/O.
func Correlate(deviations []Deviation) []MetricCorrelation {
	if len(deviations) == 0 {
		return nil
	}

	// Index deviations by metric+direction for fast lookup.
	byMetric := make(map[string]Deviation, len(deviations))
	deviatedMetrics := make(map[string]bool, len(deviations))
	for _, d := range deviations {
		key := d.Metric + ":" + d.Direction
		byMetric[key] = d
		deviatedMetrics[d.Metric] = true
	}

	var results []MetricCorrelation
	for _, p := range patterns {
		var matched []string
		var matchedDevs []Deviation
		for _, sig := range p.signals {
			key := sig.metric + ":" + sig.direction
			if d, ok := byMetric[key]; ok {
				matched = append(matched, d.String())
				matchedDevs = append(matchedDevs, d)
			}
		}
		// CDN pressure has a flexible 3rd signal: rebuffer↑ OR buffer_health↓
		if p.pattern == PatternCDNPressure && len(matched) >= 2 {
			if d, ok := byMetric["total_rebuffer_count:above"]; ok {
				matched = append(matched, d.String())
			} else if d, ok := byMetric["avg_buffer_health:below"]; ok {
				matched = append(matched, d.String())
			}
		}

		if len(matched) < 2 {
			continue
		}

		totalSignals := len(p.signals)
		if p.pattern == PatternCDNPressure {
			totalSignals = 3
		}
		confidence := float64(len(matched)) / float64(totalSignals)

		// Absence boost: if a metric expected in another pattern is NOT deviated,
		// it strengthens this hypothesis.
		for _, absent := range p.absenceBoost {
			if !deviatedMetrics[absent] {
				confidence = min(confidence+0.1, 1.0)
			}
		}

		hypothesis := buildHypothesis(p.pattern, matchedDevs)
		results = append(results, MetricCorrelation{
			Pattern:    p.pattern,
			Confidence: confidence,
			Signals:    matched,
			Hypothesis: hypothesis,
		})
	}

	return results
}

func buildHypothesis(pattern CorrelationPattern, devs []Deviation) string {
	label := strings.ReplaceAll(string(pattern), "_", " ")
	label = strings.ToUpper(label[:1]) + label[1:]

	parts := make([]string, 0, len(devs))
	for _, d := range devs {
		parts = append(parts, fmt.Sprintf("%s %.1fσ %s baseline", d.Metric, d.Sigma, d.Direction))
	}
	return fmt.Sprintf("%s: %s", label, strings.Join(parts, ", "))
}
