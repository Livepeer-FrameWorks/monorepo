package handlers

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
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
	in := buildRatingInputFromSummary(models.UsageSummary{ViewerHours: 100}, "EUR", nil)
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
}

func TestBuildRatingInputFromSummary_IncludesPriceableOperationalMeters(t *testing.T) {
	in := buildRatingInputFromSummary(models.UsageSummary{EgressGB: 12.5, PeakBandwidthMbps: 80}, "EUR", nil)
	if got := in.Usage[rating.Meter("egress_gb")]; !got.Equal(dec("12.5")) {
		t.Errorf("egress_gb = %s, want 12.5", got)
	}
	if got := in.Usage[rating.Meter("peak_bandwidth_mbps")]; !got.Equal(dec("80")) {
		t.Errorf("peak_bandwidth_mbps = %s, want 80", got)
	}
}

func TestBuildRatingInputFromSummary_MediaSecondsDoesNotDoubleCountBreakdownRollups(t *testing.T) {
	in := buildRatingInputFromSummary(models.UsageSummary{
		ProcessingSeconds: map[string]float64{
			"Livepeer:h264": 100,
			"AV:h264":       50,
			"h264":          150,
		},
	}, "EUR", nil)
	if got := in.Usage[rating.MeterMediaSeconds]; !got.Equal(dec("150")) {
		t.Errorf("media_seconds = %s, want 150", got)
	}
	if got := in.Breakdowns[rating.MeterMediaSeconds]["h264"]; !got.Equal(dec("150")) {
		t.Errorf("h264 breakdown = %s, want 150", got)
	}
	if got := in.Breakdowns[rating.MeterMediaSeconds]["Livepeer:h264"]; !got.Equal(dec("100")) {
		t.Errorf("Livepeer:h264 breakdown = %s, want 100", got)
	}
	if got := in.Breakdowns[rating.MeterMediaSeconds]["AV:h264"]; !got.Equal(dec("50")) {
		t.Errorf("AV:h264 breakdown = %s, want 50", got)
	}
}

func TestBuildRatingInputFromSummary_IncludesUsageAdjustments(t *testing.T) {
	in := buildRatingInputFromSummary(models.UsageSummary{
		ViewerHours: 1,
		UsageAdjustments: []models.UsageAdjustment{
			{UsageType: "delivered_minutes", DeltaValue: -15},
			{
				UsageType:  "media_seconds",
				DeltaValue: -30,
				Details: models.JSONB{
					"process_type":    "Livepeer",
					"output_codec":    "h264",
					"source_event_id": "event-1",
				},
			},
		},
	}, "EUR", nil)
	if got := in.Usage[rating.MeterDeliveredMinutes]; !got.Equal(dec("45")) {
		t.Errorf("delivered_minutes = %s, want 45", got)
	}
	if got := in.Usage[rating.MeterMediaSeconds]; !got.Equal(dec("-30")) {
		t.Errorf("media_seconds = %s, want -30", got)
	}
	if got := in.Breakdowns[rating.MeterMediaSeconds]["h264"]; !got.Equal(dec("-30")) {
		t.Errorf("h264 adjustment = %s, want -30", got)
	}
	if got := in.Breakdowns[rating.MeterMediaSeconds]["Livepeer:h264"]; !got.Equal(dec("-30")) {
		t.Errorf("Livepeer:h264 adjustment = %s, want -30", got)
	}
}

func TestUsageMapFromAggregates_PassesCanonicalMetersThrough(t *testing.T) {
	usage := map[string]float64{
		"delivered_minutes":      60000,
		"storage_gb_seconds_hot": 50,
		"media_seconds":          1800,
	}
	got := usageMapFromAggregates(usage)
	if v := got[rating.MeterDeliveredMinutes]; !v.Equal(dec("60000")) {
		t.Errorf("delivered_minutes = %s, want 60000", v)
	}
	if v := got[rating.MeterStorageGBSecondsHot]; !v.Equal(dec("50")) {
		t.Errorf("storage_gb_seconds_hot = %s, want 50", v)
	}
	if v := got[rating.MeterMediaSeconds]; !v.Equal(dec("1800")) {
		t.Errorf("media_seconds = %s, want 1800", v)
	}
}

func TestCodecSecondsFromCluster_FiltersZerosButKeepsCredits(t *testing.T) {
	in := map[string]float64{"h264": 1800, "av1": 300, "hevc": 0, "vp9": -75}
	got := codecSecondsFromCluster(in)
	if v, ok := got["h264"]; !ok || !v.Equal(dec("1800")) {
		t.Errorf("h264 = %v, want 1800", v)
	}
	if v, ok := got["av1"]; !ok || !v.Equal(dec("300")) {
		t.Errorf("av1 = %v, want 300", v)
	}
	if v, ok := got["vp9"]; !ok || !v.Equal(dec("-75")) {
		t.Errorf("vp9 = %v, want -75", v)
	}
	if _, ok := got["hevc"]; ok {
		t.Errorf("hevc unexpectedly present (zero value)")
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
		"":          {"storage_gb_seconds_hot": 2.0, "delivered_minutes": 60.0},
		"cluster-a": {"delivered_minutes": 180.0},
		"cluster-b": {"delivered_minutes": 270.0, "media_seconds": 1200.0},
	}
	got := flattenUsageAcrossClusters(per)
	if got["delivered_minutes"] != 510.0 {
		t.Errorf("delivered_minutes = %v, want 510 (sum across clusters)", got["delivered_minutes"])
	}
	if got["storage_gb_seconds_hot"] != 2.0 {
		t.Errorf("storage_gb_seconds_hot = %v, want 2.0", got["storage_gb_seconds_hot"])
	}
	if got["media_seconds"] != 1200.0 {
		t.Errorf("media_seconds = %v, want 1200.0", got["media_seconds"])
	}
}

func TestClusterLineKey_LongClusterIDStaysWithinSchemaLimit(t *testing.T) {
	longClusterID := "cluster-" +
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	got := clusterLineKey("meter:storage_gb_seconds_hot", longClusterID, "202604")
	if len(got) > 128 {
		t.Fatalf("line key length = %d, want <= 128 (%q)", len(got), got)
	}
	if got == "meter:storage_gb_seconds_hot:"+longClusterID+":202604" {
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
	jm := &JobManager{db: mockDB, billing: &Service{}}
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

func TestMarketplaceLineSplitCents_HandlesNegativeCorrection(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer mockDB.Close()

	owner := uuid.New()
	jm := &JobManager{db: mockDB, billing: &Service{}}
	mock.ExpectQuery(`FROM purser\.platform_fee_policy`).
		WithArgs(owner, "cluster_metered").
		WillReturnRows(sqlmock.NewRows([]string{"fee_basis_points"}).AddRow(2000))

	operatorCents, platformCents, err := jm.marketplaceLineSplitCents(context.Background(), dec("-5.00"), &pricing.ClusterPricing{
		Kind:          pricing.KindThirdPartyMarketplace,
		OwnerTenantID: &owner,
		PricingSource: pricing.SourceClusterMetered,
	})
	if err != nil {
		t.Fatalf("marketplaceLineSplitCents: %v", err)
	}
	if operatorCents != -400 || platformCents != -100 {
		t.Fatalf("split = operator %d platform %d, want -400/-100", operatorCents, platformCents)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet expectations: %v", err)
	}
}
