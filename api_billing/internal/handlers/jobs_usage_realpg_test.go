//go:build schema_verify

package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func startPurserUsageRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-purser-usage-realpg-%d", time.Now().UnixNano())
	run := dockerpg.CLI
	t.Cleanup(func() { _, _ = run("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatalf("resolve PostgreSQL test image: %v", err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/purser.sql")
	if err != nil {
		t.Fatalf("read purser schema: %v", err)
	}
	if _, err = db.Exec(string(schema)); err != nil {
		t.Fatalf("apply purser schema: %v", err)
	}
	return db
}

func TestProcessUsageSummaryAbsentDimensions_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	jm := &JobManager{db: db, logger: logging.NewLogger(), billing: &Service{}}
	start := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	summary := models.UsageSummary{
		TenantID: "11111111-1111-4111-8111-111111111111", ClusterID: "demo-media",
		SourceID: "periscope-local", ReportID: strings.Repeat("a", 64),
		PeriodStart: start, PeriodEnd: end,
		Meters: []models.MeterQuantity{{
			Meter: "peak_bandwidth_mbps", Unit: "megabit_per_second", Quantity: 0.058208,
		}},
		ProviderUsage: []models.ProviderUsage{{
			ProviderTenantID: "22222222-2222-4222-8222-222222222222", ProviderClusterID: "provider-cluster",
			Meter: models.MeterQuantity{Meter: "peak_bandwidth_mbps", Unit: "megabit_per_second", Quantity: 0.058208},
		}},
		UsageAdjustments: []models.UsageAdjustment{{
			SourceSystem: "test", SourceID: "adjustment-1", UsageType: "peak_bandwidth_mbps", Unit: "megabit_per_second",
			ClusterID: "demo-media", DeltaValue: -0.01, PeriodStart: start, PeriodEnd: end,
		}},
	}

	if _, err := jm.processUsageSummary(context.Background(), summary, "kafka-test"); err != nil {
		t.Fatalf("process usage summary: %v", err)
	}
	expectedHash := fmt.Sprintf("%x", sha256.Sum256([]byte("{}")))
	for _, table := range []string{"usage_records", "provider_usage_records", "usage_adjustments"} {
		var dimensions, dimensionKey string
		query := fmt.Sprintf(`SELECT dimensions::text, dimension_key FROM purser.%s LIMIT 1`, table)
		if err := db.QueryRowContext(context.Background(), query).Scan(&dimensions, &dimensionKey); err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		if dimensions != "{}" || dimensionKey != expectedHash {
			t.Fatalf("%s dimensions=%q key=%q, want {} / %s", table, dimensions, dimensionKey, expectedHash)
		}
	}
}

func TestV3UsageRowsRemainImmutableOnConflictingReplay_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	jm := &JobManager{db: db, logger: logging.NewLogger(), billing: &Service{}}
	start := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	first := models.UsageSummary{
		TenantID: "11111111-1111-4111-8111-111111111111", ClusterID: "demo-media",
		SourceID: "periscope-default", ReportID: strings.Repeat("1", 64), PeriodStart: start, PeriodEnd: end,
		Meters: []models.MeterQuantity{{Meter: "egress_gb", Unit: "gibibyte", Quantity: 1}},
		ProviderUsage: []models.ProviderUsage{{
			ProviderTenantID: "provider-tenant", ProviderClusterID: "provider-cluster",
			Meter: models.MeterQuantity{Meter: "egress_gb", Unit: "gibibyte", Quantity: 2},
		}},
		UsageAdjustments: []models.UsageAdjustment{{
			SourceSystem: "periscope.projection_divergences", SourceID: "immutable-adjustment",
			UsageType: "egress_gb", Unit: "gibibyte", ClusterID: "demo-media", DeltaValue: -0.5,
			PeriodStart: start, PeriodEnd: end,
		}},
	}
	if _, err := jm.processUsageSummary(context.Background(), first, "kafka-test"); err != nil {
		t.Fatalf("persist first report: %v", err)
	}

	conflict := first
	conflict.ReportID = strings.Repeat("2", 64)
	conflict.Meters = []models.MeterQuantity{{Meter: "egress_gb", Unit: "gibibyte", Quantity: 10}}
	conflict.ProviderUsage[0].Meter.Quantity = 20
	conflict.UsageAdjustments[0].DeltaValue = -5
	if _, err := jm.processUsageSummary(context.Background(), conflict, "kafka-test"); err != nil {
		t.Fatalf("persist conflicting report: %v", err)
	}

	assertValueAndReport := func(table, valueColumn string, want float64) {
		t.Helper()
		var value float64
		var reportID string
		query := fmt.Sprintf(`SELECT %s, report_id FROM purser.%s LIMIT 1`, valueColumn, table)
		if err := db.QueryRowContext(context.Background(), query).Scan(&value, &reportID); err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		if value != want || reportID != first.ReportID {
			t.Fatalf("%s value/report = %v/%s, want %v/%s", table, value, reportID, want, first.ReportID)
		}
	}
	assertValueAndReport("usage_records", "usage_value", 1)
	assertValueAndReport("provider_usage_records", "usage_value", 2)

	var adjustment float64
	if err := db.QueryRowContext(context.Background(), `SELECT delta_value FROM purser.usage_adjustments LIMIT 1`).Scan(&adjustment); err != nil {
		t.Fatal(err)
	}
	if adjustment != -0.5 {
		t.Fatalf("adjustment value = %v, want -0.5", adjustment)
	}
}

func TestV2UsageEnvelopePersistsIdempotentlyOnV3Schema_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	jm := &JobManager{db: db, logger: logging.NewLogger(), billing: &Service{}}
	payload := []byte(`{
		"tenant_id":"11111111-1111-4111-8111-111111111111",
		"cluster_id":"demo-media",
		"source_region":"eu-west",
		"period":"2026-08-20T10:00:00Z/2026-08-20T10:05:00Z",
		"egress_gb":3,
		"storage_provider_usage":[{
			"storage_provider_tenant_id":"22222222-2222-4222-8222-222222222222",
			"storage_provider_cluster_id":"storage-eu-1",
			"storage_backend":"s3",
			"storage_scope":"cold",
			"gb_seconds":12
		}]
	}`)
	summary, source, err := decodeUsageSummary(payload)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := jm.processUsageSummary(context.Background(), summary, source); err != nil {
			t.Fatalf("persist v2 usage envelope: %v", err)
		}
	}

	var count int
	var sourceID, dimensionKey, reportID string
	var quantity float64
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*), max(source_id), max(dimension_key), max(report_id), max(usage_value)
		FROM purser.usage_records
		WHERE tenant_id = $1 AND cluster_id = $2 AND usage_type = 'egress_gb'
	`, summary.TenantID, summary.ClusterID).Scan(&count, &sourceID, &dimensionKey, &reportID, &quantity); err != nil {
		t.Fatal(err)
	}
	expectedDimensionKey := fmt.Sprintf("%x", sha256.Sum256([]byte("{}")))
	if count != 1 || sourceID != defaultMeteringSourceID || dimensionKey != expectedDimensionKey || reportID != summary.ReportID || quantity != 3 {
		t.Fatalf("persisted v2 envelope = count:%d source:%q dimension:%q report:%q quantity:%v", count, sourceID, dimensionKey, reportID, quantity)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT count(*), max(source_id), max(report_id) FROM purser.provider_usage_records`).Scan(&count, &sourceID, &reportID); err != nil {
		t.Fatal(err)
	}
	if count != 1 || sourceID != legacyMeteringSourceID || reportID != summary.ReportID {
		t.Fatalf("persisted v2 provider usage = count:%d source:%q report:%q", count, sourceID, reportID)
	}

	if _, err := db.ExecContext(context.Background(), `DELETE FROM purser.usage_records`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO purser.usage_records (
			tenant_id, cluster_id, usage_type, unit, dimensions, dimension_key,
			source_id, report_id, usage_value, usage_details,
			period_start, period_end, granularity, value_kind
		) VALUES ($1, $2, 'egress_gb', 'unit', '{}', $3, 'legacy', $4, 3, '{}', $5, $6, 'minute_5', 'delta')
	`, summary.TenantID, summary.ClusterID, strings.Repeat("0", 64), strings.Repeat("0", 64), summary.PeriodStart, summary.PeriodEnd); err != nil {
		t.Fatal(err)
	}
	accepted, err := jm.processUsageSummary(context.Background(), summary, source)
	if err != nil {
		t.Fatalf("replay against migrated v2 row: %v", err)
	}
	if len(accepted) != 1 || accepted[0].usageType != "egress_gb" || accepted[0].usageValue != 3 {
		t.Fatalf("accepted migrated usage = %+v", accepted)
	}
	var unit string
	if err := db.QueryRowContext(context.Background(), `SELECT count(*), max(source_id), max(unit), max(report_id) FROM purser.usage_records`).Scan(&count, &sourceID, &unit, &reportID); err != nil {
		t.Fatal(err)
	}
	if count != 1 || sourceID != legacyMeteringSourceID || unit != "gibibyte" || reportID != summary.ReportID {
		t.Fatalf("migrated replay = count:%d source:%q unit:%q report:%q", count, sourceID, unit, reportID)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM purser.provider_usage_records`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("provider replay created %d rows, want 1", count)
	}
}

func TestV2ProviderReplayAdoptsBackfilledEmptyDimensions_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	jm := &JobManager{db: db, logger: logging.NewLogger(), billing: &Service{}}
	payload := []byte(`{
		"tenant_id":"11111111-1111-4111-8111-111111111111",
		"cluster_id":"demo-media",
		"period":"2026-08-20T10:00:00Z/2026-08-20T10:05:00Z",
		"storage_provider_usage":[{
			"storage_provider_tenant_id":"22222222-2222-4222-8222-222222222222",
			"storage_provider_cluster_id":"storage-eu-1",
			"gb_seconds":12
		}]
	}`)
	summary, source, err := decodeUsageSummary(payload)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO purser.provider_usage_records (
			usage_tenant_id, work_cluster_id, provider_tenant_id, provider_cluster_id,
			usage_type, unit, usage_value, dimensions, dimension_key, source_id,
			report_id, period_start, period_end, granularity, value_kind, source
		) VALUES (
			$1, $2, $3, $4, 'storage_gb_seconds_hot', 'gibibyte_second', 12,
			jsonb_build_object('storage_backend', '', 'storage_scope', ''),
			encode(digest(jsonb_build_object('storage_backend', '', 'storage_scope', '')::text, 'sha256'), 'hex'),
			'legacy', repeat('0', 64), $5, $6, 'minute_5', 'delta', 'kafka'
		)
	`, summary.TenantID, summary.ClusterID,
		summary.ProviderUsage[0].ProviderTenantID, summary.ProviderUsage[0].ProviderClusterID,
		summary.PeriodStart, summary.PeriodEnd); err != nil {
		t.Fatal(err)
	}

	if _, err := jm.processUsageSummary(context.Background(), summary, source); err != nil {
		t.Fatalf("replay legacy provider usage: %v", err)
	}

	var count int
	var reportID string
	if err := db.QueryRowContext(context.Background(), `
		SELECT count(*), max(report_id) FROM purser.provider_usage_records
	`).Scan(&count, &reportID); err != nil {
		t.Fatal(err)
	}
	if count != 1 || reportID != summary.ReportID {
		t.Fatalf("provider rows = %d, report = %q; want one adopted row with report %q", count, reportID, summary.ReportID)
	}
}

func TestMeteringSourceRegionRemainsAuthoritative_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	jm := &JobManager{db: db, logger: logging.NewLogger(), billing: &Service{}}
	start := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	summary := models.UsageSummary{
		TenantID:     "11111111-1111-4111-8111-111111111111",
		ClusterID:    "demo-media",
		SourceID:     "periscope-default",
		SourceRegion: "eu-west",
		ReportID:     strings.Repeat("e", 64),
		ReportKind:   "window_complete",
		Sequence:     1,
		PeriodStart:  start,
		PeriodEnd:    start.Add(5 * time.Minute),
		Complete:     true,
	}
	if err := jm.processWindowCompletion(context.Background(), summary); err != nil {
		t.Fatalf("register metering source: %v", err)
	}

	summary.SourceRegion = "us-east"
	summary.ReportID = strings.Repeat("f", 64)
	summary.Sequence++
	summary.PeriodStart = summary.PeriodEnd
	summary.PeriodEnd = summary.PeriodStart.Add(5 * time.Minute)
	if err := jm.processWindowCompletion(context.Background(), summary); err == nil || !strings.Contains(err.Error(), "registered in region") {
		t.Fatalf("region mismatch error = %v", err)
	}

	var region string
	if err := db.QueryRowContext(context.Background(), `
		SELECT region FROM purser.metering_sources WHERE source_id = $1
	`, summary.SourceID).Scan(&region); err != nil {
		t.Fatal(err)
	}
	if region != "eu-west" {
		t.Fatalf("persisted source region = %q, want eu-west", region)
	}
}

func TestPrepaidUsageSettlementMatchesAppliedBalanceTransactions_RealPG(t *testing.T) {
	t.Setenv("WAIVE_USAGE_CHARGES", "false")
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	periodStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)

	seedTenant := func(t *testing.T) (string, *JobManager) {
		t.Helper()
		tenantID, tierID, subscriptionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin prepaid tenant seed: %v", err)
		}
		defer func() { _ = tx.Rollback() }()
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO purser.billing_tiers (id, tier_name, display_name, currency, metering_enabled)
			VALUES ($1, $2, 'Metering settlement test', 'EUR', TRUE)
		`, tierID, "metering-"+tierID); err != nil {
			t.Fatalf("seed billing tier: %v", err)
		}
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO purser.tier_pricing_rules
				(tier_id, meter, model, currency, included_quantity, unit_price, config)
			VALUES ($1, 'egress_gb', 'all_usage', 'EUR', 0, 0.01, '{}')
		`, tierID); err != nil {
			t.Fatalf("seed pricing rule: %v", err)
		}
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO purser.tenant_subscriptions
				(id, tenant_id, tier_id, status, billing_model, billing_period_start, billing_period_end)
			VALUES ($1, $2, $3, 'active', 'prepaid', $4, $5)
		`, subscriptionID, tenantID, tierID, periodStart, periodEnd); err != nil {
			t.Fatalf("seed tenant subscription: %v", err)
		}
		if _, err = tx.ExecContext(ctx, `
			INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency)
			VALUES ($1, 1000, 'EUR')
		`, tenantID); err != nil {
			t.Fatalf("seed prepaid balance: %v", err)
		}
		if err = tx.Commit(); err != nil {
			t.Fatalf("commit prepaid tenant seed: %v", err)
		}
		return tenantID, &JobManager{db: db, logger: logging.NewLogger(), billing: &Service{}}
	}

	upsertUsage := func(t *testing.T, tenantID, reportID string, start time.Time, quantity float64) models.UsageSummary {
		t.Helper()
		end := start.Add(5 * time.Minute)
		dimensionKey := fmt.Sprintf("%x", sha256.Sum256([]byte("{}")))
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.usage_records (
				tenant_id, cluster_id, usage_type, unit, dimensions, dimension_key,
				source_id, report_id, usage_value, usage_details,
				period_start, period_end, granularity, value_kind
			) VALUES ($1, '', 'egress_gb', 'gibibyte', '{}', $2,
				'periscope-default', $3, $4, '{}', $5, $6, 'minute_5', 'delta')
			ON CONFLICT (
				tenant_id, cluster_id, source_id, usage_type, dimension_key, period_start, period_end
			) DO UPDATE SET usage_value = EXCLUDED.usage_value, report_id = EXCLUDED.report_id
		`, tenantID, dimensionKey, reportID, quantity, start, end); err != nil {
			t.Fatalf("upsert usage: %v", err)
		}
		return models.UsageSummary{
			TenantID: tenantID, ClusterID: "", SourceID: "periscope-default", ReportID: reportID,
			PeriodStart: start, PeriodEnd: end,
		}
	}

	settle := func(jm *JobManager, summary models.UsageSummary) error {
		return jm.processPrepaidUsage(ctx, summary, []canonicalUsageDelta{{usageType: "egress_gb", usageValue: 1}})
	}
	assertLedger := func(t *testing.T, tenantID string, wantBalance, wantSettlementMicro, wantTransactionCents int64, wantSettlements int) {
		t.Helper()
		var balance, settlementMicro, transactionCents int64
		var settlements int
		if err := db.QueryRowContext(ctx, `
			SELECT balance_cents FROM purser.prepaid_balances WHERE tenant_id = $1 AND currency = 'EUR'
		`, tenantID).Scan(&balance); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(amount_micro), 0), count(*)
			FROM purser.prepaid_usage_settlements WHERE tenant_id = $1
		`, tenantID).Scan(&settlementMicro, &settlements); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(amount_cents), 0)
			FROM purser.balance_transactions
			WHERE tenant_id = $1 AND reference_type = 'usage_summary'
		`, tenantID).Scan(&transactionCents); err != nil {
			t.Fatal(err)
		}
		if balance != wantBalance || settlementMicro != wantSettlementMicro || transactionCents != wantTransactionCents || settlements != wantSettlements {
			t.Fatalf("ledger = balance:%d settlement:%d transaction:%d rows:%d, want %d/%d/%d/%d",
				balance, settlementMicro, transactionCents, settlements,
				wantBalance, wantSettlementMicro, wantTransactionCents, wantSettlements)
		}
	}

	t.Run("corrections and replay", func(t *testing.T) {
		tenantID, jm := seedTenant(t)
		start := periodStart.Add(time.Hour)
		first := upsertUsage(t, tenantID, strings.Repeat("1", 64), start, 10)
		if err := settle(jm, first); err != nil {
			t.Fatalf("settle first report: %v", err)
		}
		assertLedger(t, tenantID, 990, 100000, -10, 1)

		increased := upsertUsage(t, tenantID, strings.Repeat("2", 64), start, 20)
		if err := settle(jm, increased); err != nil {
			t.Fatalf("settle upward correction: %v", err)
		}
		assertLedger(t, tenantID, 980, 200000, -20, 2)

		decreased := upsertUsage(t, tenantID, strings.Repeat("3", 64), start, 5)
		if err := settle(jm, decreased); err != nil {
			t.Fatalf("settle negative correction: %v", err)
		}
		if err := settle(jm, decreased); err != nil {
			t.Fatalf("replay corrected report: %v", err)
		}
		assertLedger(t, tenantID, 995, 50000, -5, 3)
	})

	t.Run("concurrent reports serialize cumulative settlement", func(t *testing.T) {
		tenantID, jm := seedTenant(t)
		first := upsertUsage(t, tenantID, strings.Repeat("4", 64), periodStart.Add(2*time.Hour), 10)
		second := upsertUsage(t, tenantID, strings.Repeat("5", 64), periodStart.Add(3*time.Hour), 20)
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, summary := range []models.UsageSummary{first, second} {
			wg.Add(1)
			go func(summary models.UsageSummary) {
				defer wg.Done()
				errs <- settle(jm, summary)
			}(summary)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent settlement: %v", err)
			}
		}
		assertLedger(t, tenantID, 970, 300000, -30, 2)
	})

	t.Run("settlement failure rolls back balance and ledger", func(t *testing.T) {
		tenantID, jm := seedTenant(t)
		reportID := strings.Repeat("6", 64)
		summary := upsertUsage(t, tenantID, reportID, periodStart.Add(4*time.Hour), 10)
		if _, err := db.ExecContext(ctx, `
			CREATE FUNCTION purser.reject_test_prepaid_settlement() RETURNS trigger AS $$
			BEGIN
				RAISE EXCEPTION 'forced prepaid settlement failure';
			END;
			$$ LANGUAGE plpgsql
		`); err != nil {
			t.Fatalf("create settlement failure function: %v", err)
		}
		if _, err := db.ExecContext(ctx, `
			CREATE TRIGGER reject_test_prepaid_settlement
			BEFORE INSERT ON purser.prepaid_usage_settlements
			FOR EACH ROW EXECUTE FUNCTION purser.reject_test_prepaid_settlement()
		`); err != nil {
			t.Fatalf("create settlement failure trigger: %v", err)
		}
		if err := settle(jm, summary); err == nil || !strings.Contains(err.Error(), "forced prepaid settlement failure") {
			t.Fatalf("settlement failure = %v", err)
		}
		assertLedger(t, tenantID, 1000, 0, 0, 0)
	})
}
