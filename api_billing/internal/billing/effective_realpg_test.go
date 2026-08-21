//go:build schema_verify

package billing

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/rating"
)

func startEffectiveTierRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-effective-tier-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
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
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/purser.sql")
	if err != nil {
		t.Fatalf("read Purser schema: %v", err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatalf("apply Purser schema: %v", err)
	}
	return db
}

func TestLoadEffectiveTierPartialOverrides_RealPG(t *testing.T) {
	db := startEffectiveTierRealPG(t)
	ctx := context.Background()
	tenantID := uuid.MustParse("71000000-0000-4000-8000-000000000001")
	tierID := uuid.MustParse("72000000-0000-4000-8000-000000000001")
	subscriptionID := uuid.MustParse("73000000-0000-4000-8000-000000000001")

	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO purser.billing_tiers
			(id, tier_name, display_name, base_price, currency, metering_enabled)
		  VALUES ($1, 'effective-contract', 'Effective Contract', 10, 'EUR', true)`, []any{tierID}},
		{`INSERT INTO purser.tenant_subscriptions
			(id, tenant_id, tier_id, status)
		  VALUES ($1, $2, $3, 'active')`, []any{subscriptionID, tenantID, tierID}},
		{`INSERT INTO purser.tier_pricing_rules
			(id, tier_id, meter, model, currency, included_quantity, unit_price, config)
		  VALUES (gen_random_uuid(), $1, 'egress_gb', 'tiered_graduated', 'EUR', 100, 0.05, '{}')`, []any{tierID}},
		{`INSERT INTO purser.subscription_pricing_overrides
			(subscription_id, meter, model, currency, included_quantity, unit_price, config)
		  VALUES ($1, 'egress_gb', NULL, NULL, NULL, 0.03, NULL)`, []any{subscriptionID}},
		{`INSERT INTO purser.tier_entitlements (tier_id, key, value)
		  VALUES ($1, 'retention_days', '5')`, []any{tierID}},
		{`INSERT INTO purser.subscription_entitlement_overrides (subscription_id, key, value)
		  VALUES ($1, 'retention_days', '10')`, []any{subscriptionID}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed effective tier contract: %v", err)
		}
	}

	effective, err := LoadEffectiveTier(ctx, db, tenantID.String())
	if err != nil {
		t.Fatalf("LoadEffectiveTier: %v", err)
	}
	if effective.TierID != tierID.String() || effective.TierName != "effective-contract" || !effective.MeteringEnabled {
		t.Fatalf("effective tier identity = %+v", effective)
	}
	if !effective.BasePrice.Equal(decimal.NewFromInt(10)) || effective.Currency != "EUR" {
		t.Fatalf("effective tier money = %s %s", effective.BasePrice, effective.Currency)
	}
	if len(effective.Rules) != 1 {
		t.Fatalf("rules = %+v", effective.Rules)
	}
	rule := effective.Rules[0]
	if rule.Meter != rating.MeterEgressGB || rule.Model != rating.ModelTieredGraduated ||
		!rule.IncludedQuantity.Equal(decimal.NewFromInt(100)) ||
		!rule.UnitPrice.Equal(decimal.RequireFromString("0.03")) {
		t.Fatalf("merged rule = %+v", rule)
	}
	if effective.Entitlements["retention_days"] != "10" {
		t.Fatalf("entitlements = %+v", effective.Entitlements)
	}
}
