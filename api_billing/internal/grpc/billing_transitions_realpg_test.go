//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"sync"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

func startPurserTransitionRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-purser-transition-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", "pgvector/pgvector:pg15"); err != nil {
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

func TestBillingTransitionsSerializeAndPreserveCredit_RealPG(t *testing.T) { //nolint:funlen // One database exercises the complete transition race matrix.
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	tiers := map[string]string{
		"prepaid": uuid.NewString(), "free": uuid.NewString(),
		"paid_a": uuid.NewString(), "paid_b": uuid.NewString(),
	}
	for name, level := range map[string]int{"prepaid": 0, "free": 1, "paid_a": 2, "paid_b": 3} {
		tierName := "transition-" + name + "-" + uuid.NewString()
		if name == "free" {
			tierName = "free"
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.billing_tiers (
				id, tier_name, display_name, tier_level, is_default_prepaid, is_default_postpaid
			) VALUES ($1,$2,$3,$4,$5,$6)
		`, tiers[name], tierName, name, level, name == "prepaid", name == "free"); err != nil {
			t.Fatal(err)
		}
	}
	server := &PurserServer{db: db, logger: logging.NewLogger()}

	seed := func(t *testing.T) string {
		t.Helper()
		tenantID := uuid.NewString()
		periodStart := time.Now().UTC().Add(-24 * time.Hour)
		periodEnd := periodStart.AddDate(0, 1, 0)
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.tenant_subscriptions (
				id, tenant_id, tier_id, billing_model, status, payment_method,
				stripe_subscription_id, billing_email, billing_name, billing_address,
				billing_period_start, billing_period_end
			) VALUES ($1,$2,$3,'prepaid','active','stripe',$4,$5,$6,$7::jsonb,$8,$9);
		`, uuid.NewString(), tenantID, tiers["prepaid"], "sub_confirmed", "billing@example.com", "Customer Name",
			`{"street":"Main 1","city":"Leiden","postal_code":"2332 ED","country":"NL"}`,
			periodStart, periodEnd); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `
			INSERT INTO purser.prepaid_balances (id, tenant_id, balance_cents, currency)
			VALUES ($1,$2,1700,'EUR')
		`, uuid.NewString(), tenantID); err != nil {
			t.Fatal(err)
		}
		return tenantID
	}

	assertFinal := func(t *testing.T, tenantID string, allowedTiers ...string) {
		t.Helper()
		var model, tierID string
		var balance int64
		if err := db.QueryRowContext(ctx, `
			SELECT s.billing_model, s.tier_id::text, b.balance_cents
			FROM purser.tenant_subscriptions s
			JOIN purser.prepaid_balances b ON b.tenant_id=s.tenant_id AND b.currency='EUR'
			WHERE s.tenant_id=$1
		`, tenantID).Scan(&model, &tierID, &balance); err != nil {
			t.Fatal(err)
		}
		allowed := false
		for _, candidate := range allowedTiers {
			allowed = allowed || tierID == candidate
		}
		if model != "postpaid" || !allowed || balance != 1700 {
			t.Fatalf("final model/tier/balance=%s/%s/%d allowed=%v", model, tierID, balance, allowedTiers)
		}
	}

	runPromotions := func(tenantID, leftTier, rightTier string) []error {
		start := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for index, tierID := range []string{leftTier, rightTier} {
			wg.Add(1)
			go func(index int, tierID string) {
				defer wg.Done()
				<-start
				_, errs[index] = server.PromoteToPaid(ctx, &purserpb.PromoteToPaidRequest{TenantId: tenantID, TierId: tierID})
			}(index, tierID)
		}
		close(start)
		wg.Wait()
		return errs
	}

	t.Run("Free versus paid promotion", func(t *testing.T) {
		tenantID := seed(t)
		errs := runPromotions(tenantID, tiers["free"], tiers["paid_a"])
		if errs[0] != nil && errs[1] != nil {
			t.Fatalf("both transitions failed: %v / %v", errs[0], errs[1])
		}
		assertFinal(t, tenantID, tiers["free"], tiers["paid_a"])
	})

	t.Run("two paid tiers", func(t *testing.T) {
		tenantID := seed(t)
		errs := runPromotions(tenantID, tiers["paid_a"], tiers["paid_b"])
		if errs[0] != nil && errs[1] != nil {
			t.Fatalf("both transitions failed: %v / %v", errs[0], errs[1])
		}
		assertFinal(t, tenantID, tiers["paid_a"], tiers["paid_b"])
	})

	t.Run("duplicate Free activation", func(t *testing.T) {
		tenantID := seed(t)
		for index, err := range runPromotions(tenantID, tiers["free"], tiers["free"]) {
			if err != nil {
				t.Fatalf("activation %d failed: %v", index, err)
			}
		}
		assertFinal(t, tenantID, tiers["free"])
	})

	t.Run("promotion racing ordinary tier change", func(t *testing.T) {
		tenantID := seed(t)
		start := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, errs[0] = server.PromoteToPaid(ctx, &purserpb.PromoteToPaidRequest{TenantId: tenantID, TierId: tiers["paid_a"]})
		}()
		go func() {
			defer wg.Done()
			<-start
			_, errs[1] = server.ChangeBillingTier(ctx, &purserpb.ChangeBillingTierRequest{TenantId: tenantID, TierId: tiers["paid_b"]})
		}()
		close(start)
		wg.Wait()
		if errs[0] != nil {
			t.Fatalf("promotion failed: %v (tier change: %v)", errs[0], errs[1])
		}
		assertFinal(t, tenantID, tiers["paid_a"], tiers["paid_b"])
	})
}
