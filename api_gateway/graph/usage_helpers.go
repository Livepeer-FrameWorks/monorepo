package graph

import (
	"encoding/json"
	"math"
	"strings"
	"time"

	"frameworks/pkg/proto"
)

func usageSummaryFromInvoice(obj *proto.Invoice) *proto.UsageSummary {
	if obj == nil || obj.UsageDetails == nil {
		return nil
	}

	details := obj.UsageDetails.AsMap()
	rawUsage, ok := details["usage_data"].(map[string]interface{})
	if !ok || rawUsage == nil {
		return nil
	}

	summary := &proto.UsageSummary{
		TenantId: obj.TenantId,
		// Core billing metrics
		StreamHours:       floatFromMap(rawUsage, "stream_hours"),
		ViewerHours:       floatFromMap(rawUsage, "viewer_hours"),
		EgressGb:          floatFromMap(rawUsage, "egress_gb"),
		RecordingGb:       floatFromMap(rawUsage, "recording_gb"),
		PeakBandwidthMbps: floatFromMap(rawUsage, "peak_bandwidth_mbps"),
		// Storage metrics
		StorageGb:            floatFromMap(rawUsage, "storage_gb"),
		AverageStorageGb:     floatFromMap(rawUsage, "average_storage_gb"),
		ClipsAdded:           intFromMap(rawUsage, "clips_added"),
		ClipsDeleted:         intFromMap(rawUsage, "clips_deleted"),
		ClipStorageAddedGb:   floatFromMap(rawUsage, "clip_storage_added_gb"),
		ClipStorageDeletedGb: floatFromMap(rawUsage, "clip_storage_deleted_gb"),
		DvrAdded:             intFromMap(rawUsage, "dvr_added"),
		DvrDeleted:           intFromMap(rawUsage, "dvr_deleted"),
		DvrStorageAddedGb:    floatFromMap(rawUsage, "dvr_storage_added_gb"),
		DvrStorageDeletedGb:  floatFromMap(rawUsage, "dvr_storage_deleted_gb"),
		VodAdded:             intFromMap(rawUsage, "vod_added"),
		VodDeleted:           intFromMap(rawUsage, "vod_deleted"),
		VodStorageAddedGb:    floatFromMap(rawUsage, "vod_storage_added_gb"),
		VodStorageDeletedGb:  floatFromMap(rawUsage, "vod_storage_deleted_gb"),
		// Viewer metrics
		TotalStreams:      intFromMap(rawUsage, "total_streams"),
		TotalViewers:      intFromMap(rawUsage, "total_viewers"),
		PeakViewers:       intFromMap(rawUsage, "peak_viewers"),
		MaxViewers:        intFromMap(rawUsage, "max_viewers"),
		UniqueUsers:       intFromMap(rawUsage, "unique_users"),
		UniqueUsersPeriod: intFromMap(rawUsage, "unique_users_period"),
		// Processing totals
		LivepeerSeconds:       floatFromMap(rawUsage, "livepeer_seconds"),
		LivepeerSegmentCount:  intFromMap(rawUsage, "livepeer_segment_count"),
		LivepeerUniqueStreams: intFromMap(rawUsage, "livepeer_unique_streams"),
		NativeAvSeconds:       floatFromMap(rawUsage, "native_av_seconds"),
		NativeAvSegmentCount:  intFromMap(rawUsage, "native_av_segment_count"),
		NativeAvUniqueStreams: intFromMap(rawUsage, "native_av_unique_streams"),
		// Per-codec breakdown
		LivepeerH264Seconds: floatFromMap(rawUsage, "livepeer_h264_seconds"),
		LivepeerVp9Seconds:  floatFromMap(rawUsage, "livepeer_vp9_seconds"),
		LivepeerAv1Seconds:  floatFromMap(rawUsage, "livepeer_av1_seconds"),
		LivepeerHevcSeconds: floatFromMap(rawUsage, "livepeer_hevc_seconds"),
		NativeAvH264Seconds: floatFromMap(rawUsage, "native_av_h264_seconds"),
		NativeAvVp9Seconds:  floatFromMap(rawUsage, "native_av_vp9_seconds"),
		NativeAvAv1Seconds:  floatFromMap(rawUsage, "native_av_av1_seconds"),
		NativeAvHevcSeconds: floatFromMap(rawUsage, "native_av_hevc_seconds"),
		NativeAvAacSeconds:  floatFromMap(rawUsage, "native_av_aac_seconds"),
		NativeAvOpusSeconds: floatFromMap(rawUsage, "native_av_opus_seconds"),
		AudioSeconds:        floatFromMap(rawUsage, "audio_seconds"),
		VideoSeconds:        floatFromMap(rawUsage, "video_seconds"),
		// Extra ClickHouse metrics
		AvgViewers:      floatFromMap(details, "avg_viewers"),
		UniqueCountries: intFromMap(details, "unique_countries"),
		UniqueCities:    intFromMap(details, "unique_cities"),
		GeoBreakdown:    parseGeoBreakdown(details["geo_breakdown"]),
	}

	if obj.PeriodStart != nil && obj.PeriodEnd != nil {
		start := obj.PeriodStart.AsTime()
		end := obj.PeriodEnd.AsTime()
		summary.Period = start.Format(time.RFC3339) + "/" + end.Format(time.RFC3339)
		summary.Granularity = deriveGranularity(start, end)
	}
	if obj.UpdatedAt != nil {
		summary.Timestamp = obj.UpdatedAt
	}

	return summary
}

func floatFromMap(m map[string]interface{}, key string) float64 {
	if m == nil {
		return 0
	}
	return floatFromAny(m[key])
}

func intFromMap(m map[string]interface{}, key string) int32 {
	return int32(math.Round(floatFromMap(m, key)))
}

func floatFromAny(v interface{}) float64 {
	switch value := v.(type) {
	case float64:
		return value
	case float32:
		return float64(value)
	case int:
		return float64(value)
	case int32:
		return float64(value)
	case int64:
		return float64(value)
	case json.Number:
		f, _ := value.Float64()
		return f
	default:
		return 0
	}
}

func parseGeoBreakdown(raw interface{}) []*proto.CountryMetrics {
	entries, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	var result []*proto.CountryMetrics
	for _, entry := range entries {
		m, ok := entry.(map[string]interface{})
		if !ok {
			continue
		}
		result = append(result, &proto.CountryMetrics{
			CountryCode: stringFromMap(m, "country_code"),
			ViewerCount: intFromMap(m, "viewer_count"),
			ViewerHours: floatFromMap(m, "viewer_hours"),
			EgressGb:    floatFromMap(m, "egress_gb"),
		})
	}
	return result
}

func stringFromMap(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func deriveGranularity(start, end time.Time) string {
	duration := end.Sub(start)
	if duration >= 28*24*time.Hour {
		return "monthly"
	}
	if duration >= 24*time.Hour {
		return "daily"
	}
	return "hourly"
}

func parsePeriodRange(period string) (*time.Time, *time.Time) {
	parts := strings.Split(period, "/")
	if len(parts) < 2 {
		return nil, nil
	}
	start, err := time.Parse(time.RFC3339, parts[0])
	if err != nil {
		return nil, nil
	}
	end, err := time.Parse(time.RFC3339, parts[1])
	if err != nil {
		return nil, nil
	}
	return &start, &end
}

func usageWithinTolerance(live *proto.LiveUsageSummary, invoice *proto.Invoice) bool {
	if live == nil || invoice == nil {
		return true
	}
	summary := usageSummaryFromInvoice(invoice)
	if summary == nil {
		return true
	}
	return withinTolerance(live.ViewerHours, summary.ViewerHours) &&
		withinTolerance(live.EgressGb, summary.EgressGb)
}

func withinTolerance(live, ledger float64) bool {
	diff := math.Abs(live - ledger)
	if ledger == 0 {
		return diff <= 0.01
	}
	return diff <= math.Max(ledger*0.01, 0.01)
}
