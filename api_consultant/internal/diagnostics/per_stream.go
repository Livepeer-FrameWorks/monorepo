package diagnostics

import (
	"context"
	"sort"

	pb "frameworks/pkg/proto"
)

const maxAnomalousStreams = 20

// StreamAnomaly captures per-stream deviations from tenant-wide baselines.
type StreamAnomaly struct {
	StreamID     string
	MaxSigma     float64
	Deviations   []Deviation
	Correlations []MetricCorrelation
}

// PerStreamAnalyzer compares individual stream metrics against tenant-wide
// baselines to identify outlier streams.
type PerStreamAnalyzer struct {
	evaluator *BaselineEvaluator
}

// NewPerStreamAnalyzer creates an analyzer using the given evaluator for baselines.
func NewPerStreamAnalyzer(evaluator *BaselineEvaluator) *PerStreamAnalyzer {
	return &PerStreamAnalyzer{evaluator: evaluator}
}

// Analyze groups per-stream metrics, compares each against the tenant-wide
// baseline (stream_id=""), runs correlation on outliers, and returns the top
// anomalous streams capped at maxAnomalousStreams.
func (a *PerStreamAnalyzer) Analyze(ctx context.Context, tenantID string, metrics []*pb.StreamHealthMetric) ([]StreamAnomaly, error) {
	if a == nil || a.evaluator == nil || len(metrics) == 0 {
		return nil, nil
	}

	// Group metrics by stream and compute per-stream averages.
	streams := groupByStream(metrics)

	var anomalies []StreamAnomaly
	for streamID, avg := range streams {
		devs, err := a.evaluator.Deviations(ctx, tenantID, "", avg)
		if err != nil {
			continue
		}
		if len(devs) == 0 {
			continue
		}
		maxSigma := 0.0
		for _, d := range devs {
			if d.Sigma > maxSigma {
				maxSigma = d.Sigma
			}
		}
		correlations := Correlate(devs)
		anomalies = append(anomalies, StreamAnomaly{
			StreamID:     streamID,
			MaxSigma:     maxSigma,
			Deviations:   devs,
			Correlations: correlations,
		})
	}

	// Sort by max sigma descending, cap at limit.
	sort.Slice(anomalies, func(i, j int) bool {
		return anomalies[i].MaxSigma > anomalies[j].MaxSigma
	})
	if len(anomalies) > maxAnomalousStreams {
		anomalies = anomalies[:maxAnomalousStreams]
	}
	return anomalies, nil
}

// groupByStream computes average metric values per stream from raw data points.
func groupByStream(metrics []*pb.StreamHealthMetric) map[string]map[string]float64 {
	type accum struct {
		sum   map[string]float64
		count int
	}
	byStream := make(map[string]*accum)

	for _, m := range metrics {
		if m == nil || m.StreamId == "" {
			continue
		}
		a, ok := byStream[m.StreamId]
		if !ok {
			a = &accum{sum: make(map[string]float64)}
			byStream[m.StreamId] = a
		}
		a.count++
		a.sum["avg_bitrate"] += float64(m.Bitrate)
		a.sum["avg_fps"] += float64(m.Fps)
		a.sum["avg_buffer_health"] += float64(m.BufferHealth)
	}

	result := make(map[string]map[string]float64, len(byStream))
	for streamID, a := range byStream {
		if a.count == 0 {
			continue
		}
		avg := make(map[string]float64, len(a.sum))
		for k, v := range a.sum {
			avg[k] = v / float64(a.count)
		}
		result[streamID] = avg
	}
	return result
}
