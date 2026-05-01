package handlers

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/pricing"
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
	in := buildRatingInputFromSummary(models.UsageSummary{ViewerHours: 100, GPUHours: 5}, "EUR", nil)
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

func TestUsageMapFromAggregates_ConvertsViewerHoursToMinutes(t *testing.T) {
	usage := map[string]float64{
		"viewer_hours":       1000,
		"average_storage_gb": 50,
		"gpu_hours":          12,
	}
	got := usageMapFromAggregates(usage)
	if v := got[rating.MeterDeliveredMinutes]; !v.Equal(dec("60000")) {
		t.Errorf("delivered_minutes = %s, want 60000 (1000h * 60)", v)
	}
	if v := got[rating.MeterAverageStorageGB]; !v.Equal(dec("50")) {
		t.Errorf("average_storage_gb = %s, want 50", v)
	}
	if v := got[rating.MeterAIGPUHours]; !v.Equal(dec("12")) {
		t.Errorf("ai_gpu_hours = %s, want 12", v)
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

func TestFlattenUsageAcrossClusters_SumsPerMeter(t *testing.T) {
	per := map[string]map[string]float64{
		"":          {"average_storage_gb": 2.0, "viewer_hours": 1.0},
		"cluster-a": {"viewer_hours": 3.0},
		"cluster-b": {"viewer_hours": 4.5, "gpu_hours": 1.0},
	}
	got := flattenUsageAcrossClusters(per)
	if got["viewer_hours"] != 8.5 {
		t.Errorf("viewer_hours = %v, want 8.5 (sum across clusters)", got["viewer_hours"])
	}
	if got["average_storage_gb"] != 2.0 {
		t.Errorf("average_storage_gb = %v, want 2.0", got["average_storage_gb"])
	}
	if got["gpu_hours"] != 1.0 {
		t.Errorf("gpu_hours = %v, want 1.0", got["gpu_hours"])
	}
}

func TestClusterLineKey_LongClusterIDStaysWithinSchemaLimit(t *testing.T) {
	longClusterID := "cluster-" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	got := clusterLineKey("meter:average_storage_gb", longClusterID, "202604")
	if len(got) > 128 {
		t.Fatalf("line key length = %d, want <= 128 (%q)", len(got), got)
	}
	if got == "meter:average_storage_gb:"+longClusterID+":202604" {
		t.Fatalf("long cluster id was not compacted")
	}
}

func TestMarketplaceLineSplitCents_UsesPlatformFeePolicy(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	owner := uuid.New()
	jm := &JobManager{db: mockDB}
	mock.ExpectQuery(`FROM purser\.platform_fee_policy`).
		WithArgs(owner, "cluster_metered").
		WillReturnRows(sqlmock.NewRows([]string{"fee_basis_points"}).AddRow(2000))

	operatorCents, platformCents, err := jm.marketplaceLineSplitCents(context.Background(), dec("5.00"), &pricing.ClusterPricing{
		Kind:          pricing.KindThirdPartyMarketplace,
		OwnerTenantID: &owner,
		PricingSource: pricing.SourceClusterMetered,
	})
	if err != nil {
		t.Fatalf("marketplaceLineSplitCents: %v", err)
	}
	if operatorCents != 400 || platformCents != 100 {
		t.Fatalf("split = operator %d platform %d, want 400/100", operatorCents, platformCents)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
