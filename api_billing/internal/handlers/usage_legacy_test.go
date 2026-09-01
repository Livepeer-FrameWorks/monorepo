package handlers

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
)

func TestDecodeUsageSummaryConvertsV2Envelope(t *testing.T) {
	payload := []byte(`{
		"tenant_id":"10000000-0000-4000-8000-000000000001",
		"cluster_id":"edge-eu-1",
		"source_region":"eu-west",
		"period":"2026-08-20T10:00:00Z/2026-08-20T10:05:00Z",
		"viewer_hours":2,
		"egress_gb":3,
		"processing_seconds":{"AV:h264":10,"h264":10},
		"meters":{"egress_gb":99,"api_requests":7},
		"storage_provider_usage":[{
			"customer_cluster_id":"edge-eu-1",
			"storage_provider_tenant_id":"provider-1",
			"storage_provider_cluster_id":"storage-1",
			"storage_backend":"s3",
			"storage_scope":"cold",
			"gb_seconds":12
		}]
	}`)

	summary, source, err := decodeUsageSummary(payload)
	if err != nil {
		t.Fatal(err)
	}
	if source != legacyKafkaSource || summary.SourceID != defaultMeteringSourceID {
		t.Fatalf("source = %q/%q", source, summary.SourceID)
	}
	if summary.ReportKind != "finalized" || !summary.Complete || len(summary.ReportID) != 64 {
		t.Fatalf("envelope = %+v", summary)
	}
	if got := summary.PeriodEnd.Sub(summary.PeriodStart); got != 5*time.Minute {
		t.Fatalf("period = %s", got)
	}
	if got := meterQuantity(summary.Meters, "delivered_minutes"); got != 120 {
		t.Fatalf("delivered_minutes = %v", got)
	}
	if got := meterQuantity(summary.Meters, "egress_gb"); got != 3 {
		t.Fatalf("egress_gb = %v, want producer field to override legacy meters map", got)
	}
	if got := meterQuantity(summary.Meters, "api_requests"); got != 7 {
		t.Fatalf("api_requests = %v", got)
	}
	if got := meterQuantity(summary.Meters, "media_seconds"); got != 10 {
		t.Fatalf("media_seconds = %v", got)
	}
	if len(summary.ProviderUsage) != 1 || summary.ProviderUsage[0].Meter.Unit != "gibibyte_second" {
		t.Fatalf("provider usage = %+v", summary.ProviderUsage)
	}

	replayed, _, err := decodeUsageSummary(payload)
	if err != nil || replayed.ReportID != summary.ReportID {
		t.Fatalf("replayed report id = %q, %v", replayed.ReportID, err)
	}
	if got := usageDimensionKey([]byte(`{}`)); got == strings.Repeat("0", 64) {
		t.Fatalf("converted v2 report retained the migrated-row sentinel key: %q", got)
	}
	legacyReference := usageSummaryReferenceID(summary)
	wantReference := uuid.NewSHA1(uuid.NameSpaceOID, []byte(summary.ReportID)).String()
	if legacyReference.String() != wantReference {
		t.Fatalf("legacy prepaid reference = %s, want report reference %s", legacyReference, wantReference)
	}
	corrected := summary
	corrected.ReportID = strings.Repeat("f", 64)
	if usageSummaryReferenceID(corrected) == legacyReference {
		t.Fatal("corrected report reused the original prepaid transaction identity")
	}
}

func TestDecodeUsageSummaryLeavesV3EnvelopeUnchanged(t *testing.T) {
	want := models.UsageSummary{
		ReportID:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ReportKind: "finalized", SourceID: "periscope-default", SourceRegion: "eu-west",
		Sequence: 1, TenantID: "10000000-0000-4000-8000-000000000001", ClusterID: "edge-eu-1",
		PeriodStart: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC),
		PeriodEnd:   time.Date(2026, 8, 20, 10, 5, 0, 0, time.UTC), Complete: true,
	}
	payload, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	got, source, err := decodeUsageSummary(payload)
	if err != nil {
		t.Fatal(err)
	}
	if source != "kafka" || got.ReportID != want.ReportID || got.SourceID != want.SourceID {
		t.Fatalf("decoded = %+v, source = %q", got, source)
	}
	if usageDimensionKey([]byte(`{}`)) == strings.Repeat("0", 64) {
		t.Fatal("v3 report used the legacy dimension key")
	}
}

func TestDecodeUsageSummaryRejectsUnsupportedV2Meter(t *testing.T) {
	payload := []byte(`{
		"tenant_id":"10000000-0000-4000-8000-000000000001",
		"cluster_id":"edge-eu-1",
		"period":"2026-08-20T10:00:00Z/2026-08-20T10:05:00Z",
		"meters":{"future_meter":1}
	}`)
	if _, _, err := decodeUsageSummary(payload); err == nil {
		t.Fatal("expected unsupported legacy meter to fail closed")
	}
}

func TestDecodeUsageSummaryPreservesEmptyProviderDimensions(t *testing.T) {
	payload := []byte(`{
		"tenant_id":"10000000-0000-4000-8000-000000000001",
		"cluster_id":"edge-eu-1",
		"period":"2026-08-20T10:00:00Z/2026-08-20T10:05:00Z",
		"storage_provider_usage":[{
			"storage_provider_tenant_id":"provider-1",
			"storage_provider_cluster_id":"storage-1",
			"gb_seconds":12
		}]
	}`)

	summary, err := convertLegacyUsageSummary(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(summary.ProviderUsage) != 1 {
		t.Fatalf("provider usage = %+v", summary.ProviderUsage)
	}
	dimensions := summary.ProviderUsage[0].Meter.Dimensions
	if backend, ok := dimensions["storage_backend"]; !ok || backend != "" {
		t.Fatalf("storage_backend = %#v, present=%v", backend, ok)
	}
	if scope, ok := dimensions["storage_scope"]; !ok || scope != "" {
		t.Fatalf("storage_scope = %#v, present=%v", scope, ok)
	}
}

func meterQuantity(meters []models.MeterQuantity, name string) float64 {
	for _, meter := range meters {
		if meter.Meter == name {
			return meter.Quantity
		}
	}
	return 0
}
