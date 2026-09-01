package handlers

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
)

const (
	legacyMeteringSourceID  = "legacy"
	defaultMeteringSourceID = "periscope-default"
	legacyKafkaSource       = "kafka_v2_compat"
)

// legacyUsageSummaryV2 is the retained flat Periscope billing envelope. The
// consumer accepts it so a Kafka backlog can drain across an envelope upgrade.
type legacyUsageSummaryV2 struct {
	TenantID          string  `json:"tenant_id"`
	ClusterID         string  `json:"cluster_id"`
	SourceRegion      string  `json:"source_region,omitempty"`
	Period            string  `json:"period"`
	StreamHours       float64 `json:"stream_hours"`
	IngressGB         float64 `json:"ingress_gb,omitempty"`
	EgressGB          float64 `json:"egress_gb"`
	PeakBandwidthMbps float64 `json:"peak_bandwidth_mbps"`

	StorageGBSecondsHot  float64 `json:"storage_gb_seconds_hot,omitempty"`
	StorageGBSecondsCold float64 `json:"storage_gb_seconds_cold,omitempty"`

	LivepeerH264Seconds float64            `json:"livepeer_h264_seconds"`
	LivepeerVP9Seconds  float64            `json:"livepeer_vp9_seconds"`
	LivepeerAV1Seconds  float64            `json:"livepeer_av1_seconds"`
	LivepeerHEVCSeconds float64            `json:"livepeer_hevc_seconds"`
	NativeAvH264Seconds float64            `json:"native_av_h264_seconds"`
	NativeAvVP9Seconds  float64            `json:"native_av_vp9_seconds"`
	NativeAvAV1Seconds  float64            `json:"native_av_av1_seconds"`
	NativeAvHEVCSeconds float64            `json:"native_av_hevc_seconds"`
	NativeAvAACSeconds  float64            `json:"native_av_aac_seconds"`
	NativeAvOpusSeconds float64            `json:"native_av_opus_seconds"`
	ProcessingSeconds   map[string]float64 `json:"processing_seconds,omitempty"`

	TotalStreams int     `json:"total_streams"`
	TotalViewers int     `json:"total_viewers"`
	ViewerHours  float64 `json:"viewer_hours"`
	MaxViewers   int     `json:"max_viewers"`
	UniqueUsers  int     `json:"unique_users"`

	APIRequests   float64 `json:"api_requests"`
	APIErrors     float64 `json:"api_errors"`
	APIDurationMs float64 `json:"api_duration_ms"`
	APIComplexity float64 `json:"api_complexity"`

	Meters               map[string]float64             `json:"meters,omitempty"`
	StorageProviderUsage []legacyStorageProviderUsageV2 `json:"storage_provider_usage,omitempty"`
	UsageAdjustments     []models.UsageAdjustment       `json:"usage_adjustments,omitempty"`
}

type legacyStorageProviderUsageV2 struct {
	CustomerClusterID        string  `json:"customer_cluster_id,omitempty"`
	StorageProviderTenantID  string  `json:"storage_provider_tenant_id,omitempty"`
	StorageProviderClusterID string  `json:"storage_provider_cluster_id,omitempty"`
	StorageBackend           string  `json:"storage_backend,omitempty"`
	StorageScope             string  `json:"storage_scope"`
	UsageType                string  `json:"usage_type"`
	GBSeconds                float64 `json:"gb_seconds"`
}

var legacyMeterUnits = map[string]string{
	"delivered_minutes":       "minute",
	"ingress_gb":              "gibibyte",
	"egress_gb":               "gibibyte",
	"stream_runtime_seconds":  "second",
	"storage_gb_seconds_hot":  "gibibyte_second",
	"storage_gb_seconds_cold": "gibibyte_second",
	"api_requests":            "request",
	"api_errors":              "request",
	"api_duration_ms":         "millisecond",
	"api_complexity":          "point",
	"peak_bandwidth_mbps":     "megabit_per_second",
	"max_viewers":             "viewer",
	"total_streams":           "stream",
	"total_viewers":           "viewer",
	"unique_users":            "user",
	"media_seconds":           "second",
}

func decodeUsageSummary(payload []byte) (models.UsageSummary, string, error) {
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(payload, &shape); err != nil {
		return models.UsageSummary{}, "", err
	}
	if _, hasLegacyPeriod := shape["period"]; hasLegacyPeriod {
		if _, hasReportID := shape["report_id"]; !hasReportID {
			summary, err := convertLegacyUsageSummary(payload)
			return summary, legacyKafkaSource, err
		}
	}

	var summary models.UsageSummary
	if err := json.Unmarshal(payload, &summary); err != nil {
		return models.UsageSummary{}, "", err
	}
	return summary, "kafka", nil
}

func convertLegacyUsageSummary(payload []byte) (models.UsageSummary, error) {
	var legacy legacyUsageSummaryV2
	if err := json.Unmarshal(payload, &legacy); err != nil {
		return models.UsageSummary{}, err
	}
	periodStart, periodEnd, err := parseLegacyUsagePeriod(legacy.Period)
	if err != nil {
		return models.UsageSummary{}, err
	}
	if legacy.TenantID == "" || legacy.ClusterID == "" {
		return models.UsageSummary{}, errors.New("legacy usage report is missing tenant_id or cluster_id")
	}

	summary := models.UsageSummary{
		ReportKind:       "finalized",
		SourceID:         defaultMeteringSourceID,
		SourceRegion:     legacy.SourceRegion,
		Sequence:         uint64(periodEnd.Unix()),
		TenantID:         legacy.TenantID,
		ClusterID:        legacy.ClusterID,
		PeriodStart:      periodStart,
		PeriodEnd:        periodEnd,
		Complete:         true,
		UsageAdjustments: legacy.UsageAdjustments,
	}
	reportMaterial := strings.Join([]string{
		summary.SourceID,
		summary.TenantID,
		summary.ClusterID,
		summary.PeriodStart.UTC().Format(time.RFC3339Nano),
		summary.PeriodEnd.UTC().Format(time.RFC3339Nano),
		summary.ReportKind,
	}, "\x00")
	reportHash := sha256.Sum256([]byte(reportMaterial))
	summary.ReportID = fmt.Sprintf("%x", reportHash[:])

	quantities := map[string]float64{
		"delivered_minutes":       legacy.ViewerHours * 60,
		"ingress_gb":              legacy.IngressGB,
		"egress_gb":               legacy.EgressGB,
		"storage_gb_seconds_hot":  legacy.StorageGBSecondsHot,
		"storage_gb_seconds_cold": legacy.StorageGBSecondsCold,
		"media_seconds":           legacyTotalCodecSeconds(legacy),
		"stream_runtime_seconds":  legacy.StreamHours * 3600,
		"peak_bandwidth_mbps":     legacy.PeakBandwidthMbps,
		"max_viewers":             float64(legacy.MaxViewers),
		"total_streams":           float64(legacy.TotalStreams),
		"total_viewers":           float64(legacy.TotalViewers),
		"unique_users":            float64(legacy.UniqueUsers),
		"api_requests":            legacy.APIRequests,
		"api_errors":              legacy.APIErrors,
		"api_duration_ms":         legacy.APIDurationMs,
		"api_complexity":          legacy.APIComplexity,
	}
	for meter, quantity := range legacy.Meters {
		if existing, exists := quantities[meter]; exists && existing != 0 {
			continue
		}
		quantities[meter] = quantity
	}
	for meter, quantity := range quantities {
		if quantity == 0 {
			continue
		}
		unit, ok := legacyMeterUnits[meter]
		if !ok {
			return models.UsageSummary{}, fmt.Errorf("legacy usage report contains unsupported meter %q", meter)
		}
		summary.Meters = append(summary.Meters, models.MeterQuantity{Meter: meter, Unit: unit, Quantity: quantity})
	}

	for _, provider := range legacy.StorageProviderUsage {
		if provider.CustomerClusterID != "" && provider.CustomerClusterID != legacy.ClusterID {
			return models.UsageSummary{}, fmt.Errorf("legacy provider usage cluster %q differs from report cluster %q", provider.CustomerClusterID, legacy.ClusterID)
		}
		usageType := provider.UsageType
		if usageType == "" {
			usageType = "storage_gb_seconds_hot"
			if provider.StorageScope == "cold" {
				usageType = "storage_gb_seconds_cold"
			}
		}
		unit, ok := legacyMeterUnits[usageType]
		if !ok {
			return models.UsageSummary{}, fmt.Errorf("legacy provider usage contains unsupported meter %q", usageType)
		}
		// The v0.3 backfill derives legacy row identity from both stored
		// columns, including empty strings. Replays must build the same JSONB
		// value so they adopt/update that row instead of inserting a duplicate.
		dimensions := models.JSONB{
			"storage_backend": provider.StorageBackend,
			"storage_scope":   provider.StorageScope,
		}
		summary.ProviderUsage = append(summary.ProviderUsage, models.ProviderUsage{
			ProviderTenantID:  provider.StorageProviderTenantID,
			ProviderClusterID: provider.StorageProviderClusterID,
			Meter: models.MeterQuantity{
				Meter: usageType, Unit: unit, Quantity: provider.GBSeconds, Dimensions: dimensions,
			},
		})
	}

	return summary, nil
}

func parseLegacyUsagePeriod(period string) (time.Time, time.Time, error) {
	parts := strings.Split(period, "/")
	if len(parts) != 2 {
		return time.Time{}, time.Time{}, errors.New("legacy usage report has invalid period")
	}
	start, err := time.Parse(time.RFC3339, parts[0])
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse legacy period start: %w", err)
	}
	end, err := time.Parse(time.RFC3339, parts[1])
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("parse legacy period end: %w", err)
	}
	if !end.After(start) {
		return time.Time{}, time.Time{}, errors.New("legacy usage report period must be positive")
	}
	return start.UTC(), end.UTC(), nil
}

func legacyTotalCodecSeconds(summary legacyUsageSummaryV2) float64 {
	if len(summary.ProcessingSeconds) > 0 {
		total := 0.0
		useJointOnly := legacyHasJointProcessingKeys(summary.ProcessingSeconds)
		for key, seconds := range summary.ProcessingSeconds {
			if useJointOnly && !legacyIsJointProcessingKey(key) {
				continue
			}
			total += seconds
		}
		return total
	}
	return summary.LivepeerH264Seconds + summary.LivepeerVP9Seconds + summary.LivepeerAV1Seconds + summary.LivepeerHEVCSeconds +
		summary.NativeAvH264Seconds + summary.NativeAvVP9Seconds + summary.NativeAvAV1Seconds + summary.NativeAvHEVCSeconds +
		summary.NativeAvAACSeconds + summary.NativeAvOpusSeconds
}

func legacyHasJointProcessingKeys(values map[string]float64) bool {
	for key := range values {
		if legacyIsJointProcessingKey(key) {
			return true
		}
	}
	return false
}

func legacyIsJointProcessingKey(key string) bool {
	_, _, ok := strings.Cut(key, ":")
	return ok
}
