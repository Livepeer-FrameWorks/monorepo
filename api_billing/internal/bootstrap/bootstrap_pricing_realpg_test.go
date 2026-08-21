//go:build schema_verify

package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func startBootstrapPricingRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-bootstrap-pricing-realpg-%d", time.Now().UnixNano())
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

func TestBootstrapPricingRepositories_RealPG(t *testing.T) {
	db := startBootstrapPricingRealPG(t)
	ctx := context.Background()

	created, err := ReconcileDefaultPlatformFeePolicy(ctx, db)
	if err != nil || len(created.Created) != 1 {
		t.Fatalf("create fee policy = %+v, %v", created, err)
	}
	noop, err := ReconcileDefaultPlatformFeePolicy(ctx, db)
	if err != nil || len(noop.Noop) != 1 {
		t.Fatalf("replay fee policy = %+v, %v", noop, err)
	}
	var feeBPS int
	if err := db.QueryRowContext(ctx, `
		SELECT fee_basis_points FROM purser.platform_fee_policy
		WHERE cluster_kind = 'third_party_marketplace' AND effective_to IS NULL
	`).Scan(&feeBPS); err != nil || feeBPS != defaultMarketplaceFeeBPS {
		t.Fatalf("fee policy bps = %d, %v", feeBPS, err)
	}

	desired := samplePricing()
	created, err = ReconcileClusterPricing(ctx, db, []ClusterPricing{desired})
	if err != nil || len(created.Created) != 1 {
		t.Fatalf("create cluster pricing = %+v, %v", created, err)
	}
	noop, err = ReconcileClusterPricing(ctx, db, []ClusterPricing{desired})
	if err != nil || len(noop.Noop) != 1 {
		t.Fatalf("replay cluster pricing = %+v, %v", noop, err)
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE purser.cluster_pricing
		SET stripe_product_id = 'prod_contract',
		    stripe_price_id_monthly = 'price_contract',
		    stripe_meter_event_name = 'meter.contract'
		WHERE cluster_id = $1
	`, desired.ClusterID); err != nil {
		t.Fatal(err)
	}
	desired.PricingModel = "monthly"
	desired.BasePrice = "19.99"
	updated, err := ReconcileClusterPricing(ctx, db, []ClusterPricing{desired})
	if err != nil || len(updated.Updated) != 1 {
		t.Fatalf("update cluster pricing = %+v, %v", updated, err)
	}

	var model, basePrice, productID, priceID, meterName string
	if err := db.QueryRowContext(ctx, `
		SELECT pricing_model, base_price::text, stripe_product_id,
		       stripe_price_id_monthly, stripe_meter_event_name
		FROM purser.cluster_pricing WHERE cluster_id = $1
	`, desired.ClusterID).Scan(&model, &basePrice, &productID, &priceID, &meterName); err != nil {
		t.Fatal(err)
	}
	if model != "monthly" || basePrice != "19.99" || productID != "prod_contract" ||
		priceID != "price_contract" || meterName != "meter.contract" {
		t.Fatalf("cluster pricing after reconcile = %q %q %q %q %q", model, basePrice, productID, priceID, meterName)
	}
}
