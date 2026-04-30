package handlers

import (
	"testing"

	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/rating"
	"frameworks/pkg/models"
)

func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestBuildRatingInputFromSummary_BasePriceIsZero(t *testing.T) {
	// Per-event prepaid path must never charge the monthly base fee.
	tier := &billing.EffectiveTier{
		Currency:        "EUR",
		BasePrice:       dec("79.00"),
		MeteringEnabled: true,
		Rules:           nil,
	}
	in := buildRatingInputFromSummary(models.UsageSummary{ViewerHours: 100, GPUHours: 5}, tier)
	if !in.BasePrice.IsZero() {
		t.Errorf("BasePrice = %s, want 0 (per-event path must never charge base fee)", in.BasePrice)
	}
	if in.Currency != "EUR" {
		t.Errorf("Currency = %q, want EUR", in.Currency)
	}
	wantMinutes := dec("100").Mul(dec("60"))
	if got := in.Usage[rating.MeterDeliveredMinutes]; !got.Equal(wantMinutes) {
		t.Errorf("delivered_minutes = %s, want %s", got, wantMinutes)
	}
	if got := in.Usage[rating.MeterAIGPUHours]; !got.Equal(dec("5")) {
		t.Errorf("ai_gpu_hours = %s, want 5 (regression: per-event path must bill GPU)", got)
	}
}

func TestBuildRatingInputFromAggregates_BasePriceFromTier(t *testing.T) {
	tier := &billing.EffectiveTier{
		Currency:        "EUR",
		BasePrice:       dec("79.00"),
		MeteringEnabled: true,
	}
	usage := map[string]float64{
		"viewer_hours":       1000,
		"average_storage_gb": 50,
		"gpu_hours":          12,
	}
	in := buildRatingInputFromAggregates(usage, tier)
	if !in.BasePrice.Equal(dec("79.00")) {
		t.Errorf("BasePrice = %s, want 79.00", in.BasePrice)
	}
	if got := in.Usage[rating.MeterDeliveredMinutes]; !got.Equal(dec("60000")) {
		t.Errorf("delivered_minutes = %s, want 60000 (1000h * 60)", got)
	}
	if got := in.Usage[rating.MeterAverageStorageGB]; !got.Equal(dec("50")) {
		t.Errorf("average_storage_gb = %s, want 50", got)
	}
	if got := in.Usage[rating.MeterAIGPUHours]; !got.Equal(dec("12")) {
		t.Errorf("ai_gpu_hours = %s, want 12", got)
	}
}

func TestCodecSecondsFromAggregates_SumsLivepeerAndNative(t *testing.T) {
	usage := map[string]float64{
		"livepeer_h264_seconds":  1200,
		"native_av_h264_seconds": 600,
		"livepeer_av1_seconds":   300,
	}
	got := codecSecondsFromAggregates(usage)
	if v, ok := got["h264"]; !ok || !v.Equal(dec("1800")) {
		t.Errorf("h264 = %v, want 1800", v)
	}
	if v, ok := got["av1"]; !ok || !v.Equal(dec("300")) {
		t.Errorf("av1 = %v, want 300", v)
	}
	if _, ok := got["hevc"]; ok {
		t.Errorf("hevc unexpectedly present (no usage data)")
	}
}

func TestCodecSecondsFromSummary_SumsBothSources(t *testing.T) {
	s := models.UsageSummary{
		LivepeerH264Seconds: 100,
		NativeAvH264Seconds: 50,
		LivepeerHEVCSeconds: 30,
		NativeAvAACSeconds:  20,
	}
	got := codecSecondsFromSummary(s)
	if v, ok := got["h264"]; !ok || !v.Equal(dec("150")) {
		t.Errorf("h264 = %v, want 150", v)
	}
	if v, ok := got["hevc"]; !ok || !v.Equal(dec("30")) {
		t.Errorf("hevc = %v, want 30", v)
	}
	if v, ok := got["aac"]; !ok || !v.Equal(dec("20")) {
		t.Errorf("aac = %v, want 20", v)
	}
}
