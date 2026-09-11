//go:build schema_verify

package pricing

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/rating"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
	"github.com/shopspring/decimal"
)

func startPlacementSnapshotRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-placement-pricing-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/purser.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestPlacementPriceBoundaries_RealPG(t *testing.T) {
	db := startPlacementSnapshotRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	queries := purserdb.New(db)
	base, err := queries.ReadPlacementSnapshotTime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	periodStart, periodEnd, pendingAt := base.Add(-time.Hour), base.Add(40*time.Second), base.Add(25*time.Second)
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, metering_enabled) VALUES ($1, 'boundary-contract', 'Boundary Contract', 10, 'EUR', true)`, snapshotTier); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.tenant_subscriptions (id, tenant_id, tier_id, status, billing_period_start, billing_period_end, pending_tier_id, pending_effective_at) VALUES ($1, $2, $3, 'active', $4, $5, $3, $6)`, snapshotSubscription, snapshotTenant, snapshotTier, periodStart, periodEnd, pendingAt); err != nil {
		t.Fatal(err)
	}
	for _, history := range []struct {
		cluster string
		from    time.Time
		until   any
	}{
		{"ends", base.Add(-time.Hour), base.Add(20 * time.Second)},
		{"starts", base.Add(10 * time.Second), nil},
		{"both", base.Add(-time.Hour), base.Add(30 * time.Second)},
		{"both", base.Add(15 * time.Second), nil},
		{"past", base.Add(-time.Hour), base.Add(-time.Second)},
		{"unrequested", base.Add(time.Second), nil},
	} {
		if _, err := db.ExecContext(ctx, `INSERT INTO purser.cluster_pricing_history (cluster_id, pricing_model, effective_from, effective_to) VALUES ($1, 'tier_inherit', $2, $3)`, history.cluster, history.from, history.until); err != nil {
			t.Fatal(err)
		}
	}
	qm := &fakeQM{clusters: map[string]*quartermasterpb.InfrastructureCluster{}}
	var owners []*ClusterOwnershipSnapshot
	for _, id := range []string{"ends", "starts", "both", "past", "none"} {
		qm.clusters[id] = &quartermasterpb.InfrastructureCluster{ClusterId: id, IsPlatformOfficial: true}
		owner, err := CaptureClusterOwnership(ctx, qm, id)
		if err != nil {
			t.Fatal(err)
		}
		owners = append(owners, owner)
	}
	snapshot, err := ReadPlacementTariffs(ctx, db, snapshotTenant, owners)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.NextPricingChange) != 3 {
		t.Fatalf("unrelated or past boundary leaked: %+v", snapshot.NextPricingChange)
	}
	for id, duration := range map[string]time.Duration{"ends": 20 * time.Second, "starts": 10 * time.Second, "both": 15 * time.Second} {
		if got := snapshot.NextPricingChange[id]; !got.Equal(base.Add(duration)) {
			t.Fatalf("boundary for %s: %v", id, got)
		}
		expiry, err := snapshot.QuoteExpiry(id, snapshot.AsOf, snapshot.AsOf.Add(time.Hour))
		if err != nil || !expiry.Equal(base.Add(duration)) {
			t.Fatalf("quote crossed observed boundary for %s: %v %v", id, expiry, err)
		}
	}
	start, end, known := snapshot.AllowancePeriod()
	if !known || !start.Equal(periodStart) || !end.Equal(periodEnd) || snapshot.Tier.SubscriptionID != snapshotSubscription {
		t.Fatal("exact subscription period lost")
	}
	if expiry, err := snapshot.QuoteExpiry("none", snapshot.AsOf, snapshot.AsOf.Add(time.Hour)); err != nil || !expiry.Equal(pendingAt) {
		t.Fatalf("pending tier boundary lost: %v %v", expiry, err)
	}
	if _, err := queries.ReadPlacementSubscriptionContext(ctx, purserdb.ReadPlacementSubscriptionContextParams{TenantID: "85000000-0000-4000-8000-000000000001", SubscriptionID: snapshotSubscription}); err != sql.ErrNoRows {
		t.Fatalf("subscription context crossed tenant: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE purser.tenant_subscriptions SET pending_tier_id = NULL, pending_effective_at = NULL WHERE tenant_id = $1 AND id = $2`, snapshotTenant, snapshotSubscription); err != nil {
		t.Fatal(err)
	}
	withoutPending, err := ReadPlacementTariffs(ctx, db, snapshotTenant, owners)
	if err != nil {
		t.Fatal(err)
	}
	if expiry, err := withoutPending.QuoteExpiry("none", withoutPending.AsOf, withoutPending.AsOf.Add(time.Hour)); err != nil || !expiry.Equal(periodEnd) {
		t.Fatalf("allowance reset boundary lost: %v %v", expiry, err)
	}
}

func TestPlacementTariffSnapshot_RealPG(t *testing.T) {
	db := startPlacementSnapshotRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, metering_enabled) VALUES ($1, 'snapshot-contract', 'Snapshot Contract', 10, 'EUR', true)`, []any{snapshotTier}},
		{`INSERT INTO purser.tenant_subscriptions (id, tenant_id, tier_id, status) VALUES ($1, $2, $3, 'active')`, []any{snapshotSubscription, snapshotTenant, snapshotTier}},
		{`INSERT INTO purser.tier_pricing_rules (id, tier_id, meter, model, currency, included_quantity, unit_price, config) VALUES (gen_random_uuid(), $1, 'egress_gb', 'all_usage', 'EUR', 0, 0.05, '{}')`, []any{snapshotTier}},
		{`INSERT INTO purser.subscription_pricing_overrides (subscription_id, meter, unit_price) VALUES ($1, 'egress_gb', 0.03)`, []any{snapshotSubscription}},
		{`INSERT INTO purser.cluster_pricing_history (cluster_id, pricing_model, currency, metered_rates, effective_from) VALUES ('market', 'metered', 'EUR', '{"egress_gb":{"model":"all_usage","unit_price":"0.08"}}', NOW() - INTERVAL '1 hour')`, nil},
	} {
		if _, err := db.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			t.Fatalf("seed snapshot fixture: %v", err)
		}
	}
	marketOwner := "84000000-0000-4000-8000-000000000001"
	observed, err := purserdb.New(db).ReadPlacementSnapshotTime(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cutoff := observed.UTC().Truncate(5 * time.Minute)
	periodStart := cutoff.Add(-time.Hour)
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE purser.tenant_subscriptions SET billing_period_start = $1, billing_period_end = $2 WHERE tenant_id = $3 AND id = $4`, []any{periodStart, cutoff.Add(time.Hour), snapshotTenant, snapshotSubscription}},
		{`UPDATE purser.tier_pricing_rules SET model = 'tiered_graduated', included_quantity = 10 WHERE tier_id = $1 AND meter = 'egress_gb'`, []any{snapshotTier}},
		{`INSERT INTO purser.metering_sources (source_id, active_from, active_until, required) VALUES ('snapshot-source', $1, $2, true)`, []any{periodStart, cutoff}},
		{`INSERT INTO purser.metering_windows (source_id, period_start, period_end, complete, report_count) SELECT 'snapshot-source', window_start, window_start + INTERVAL '5 minutes', true, 1 FROM generate_series($1::timestamptz, $2::timestamptz - INTERVAL '5 minutes', INTERVAL '5 minutes') AS window_start`, []any{periodStart, cutoff}},
		{`INSERT INTO purser.usage_records (tenant_id, cluster_id, usage_type, unit, usage_value, value_kind, dimension_key, source_id, report_id, period_start, period_end, granularity) VALUES ($1, 'official', 'egress_gb', 'gibibyte', 20, 'delta', repeat('0',64), 'snapshot-source', 'snapshot-usage', $2, $3, 'minute_5')`, []any{snapshotTenant, periodStart, periodStart.Add(5 * time.Minute)}},
	} {
		if _, err := db.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	qm := &fakeQM{clusters: map[string]*quartermasterpb.InfrastructureCluster{
		"market":   {ClusterId: "market", OwnerTenantId: &marketOwner},
		"official": {ClusterId: "official", IsPlatformOfficial: true},
	}}
	var owners []*ClusterOwnershipSnapshot
	for _, clusterID := range []string{"official", "market"} {
		owner, err := CaptureClusterOwnership(ctx, qm, clusterID)
		if err != nil {
			t.Fatal(err)
		}
		owners = append(owners, owner)
	}
	// The reader establishes its MVCC snapshot at the database clock query, then
	// blocks on tier rules until this writer commits a coordinated tariff change.
	writer, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if rollbackErr := writer.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			t.Logf("snapshot fixture writer rollback: %v", rollbackErr)
		}
	}()
	if _, err := writer.ExecContext(ctx, `LOCK TABLE purser.tier_pricing_rules IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	type result struct {
		snapshot *PlacementTariffSnapshot
		err      error
	}
	done := make(chan result, 1)
	go func() {
		snapshot, err := ReadPlacementTariffs(ctx, db, snapshotTenant, owners)
		done <- result{snapshot, err}
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	waiting := false
	for !waiting {
		select {
		case early := <-done:
			t.Fatalf("reader did not block at tariff boundary: %+v", early)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
			if err := db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE datname = current_database() AND pid <> pg_backend_pid() AND wait_event_type = 'Lock' AND query LIKE '%name: ListTierPricingRules%')`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE purser.billing_tiers SET base_price = 20, metering_enabled = false WHERE id = $1`, []any{snapshotTier}},
		{`UPDATE purser.subscription_pricing_overrides SET unit_price = 0.07 WHERE subscription_id = $1 AND meter = 'egress_gb'`, []any{snapshotSubscription}},
		{`UPDATE purser.cluster_pricing_history SET metered_rates = '{"egress_gb":{"model":"all_usage","unit_price":"0.09"}}' WHERE cluster_id = 'market'`, nil},
		{`UPDATE purser.usage_records SET usage_value = 30 WHERE tenant_id = $1 AND report_id = 'snapshot-usage'`, []any{snapshotTenant}},
	} {
		if _, err := writer.ExecContext(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Commit(); err != nil {
		t.Fatal(err)
	}
	var old result
	select {
	case old = <-done:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	check := func(snapshot *PlacementTariffSnapshot, metering bool, base, official, market string) {
		t.Helper()
		if snapshot == nil || snapshot.TenantID != snapshotTenant || snapshot.AsOf.IsZero() || snapshot.Tier.MeteringEnabled != metering || !snapshot.Tier.BasePrice.Equal(decimal.RequireFromString(base)) {
			t.Fatalf("wrong tier snapshot: %+v", snapshot)
		}
		for clusterID, expected := range map[string]string{"official": official, "market": market} {
			cluster := snapshot.Clusters[clusterID]
			if cluster == nil || len(cluster.MeteredRules) != 1 || !cluster.MeteredRules[0].UnitPrice.Equal(decimal.RequireFromString(expected)) {
				t.Fatalf("mixed tariff for %s: %+v", clusterID, cluster)
			}
		}
	}
	if old.err != nil {
		t.Fatal(old.err)
	}
	check(old.snapshot, true, "10", "0.03", "0.08")
	if old.snapshot.AllowanceUsage.Status != PlacementUsageCovered || old.snapshot.AllowanceUsage.Totals["official"][rating.MeterEgressGB].String() != "20" {
		t.Fatalf("old tariff mixed with new consumption: %+v", old.snapshot.AllowanceUsage)
	}
	next, err := ReadPlacementTariffs(ctx, db, snapshotTenant, owners)
	if err != nil {
		t.Fatal(err)
	}
	check(next, false, "20", "0.07", "0.09")
	if next.AllowanceUsage.Status != PlacementUsageCovered || next.AllowanceUsage.Totals["official"][rating.MeterEgressGB].String() != "30" {
		t.Fatalf("new tariff retained old consumption: %+v", next.AllowanceUsage)
	}
	if !next.AsOf.After(old.snapshot.AsOf) {
		t.Fatal("new snapshot did not obtain a new database clock")
	}
	if got, err := ReadPlacementTariffs(ctx, db, "85000000-0000-4000-8000-000000000001", owners); err != sql.ErrNoRows || got != nil {
		t.Fatalf("cross-tenant tier leaked: %+v %v", got, err)
	}
}
