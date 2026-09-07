//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"frameworks/api_tenants/internal/database/quartermasterdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billingentitlements"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func TestCompleteTenantDNSEntitlementHandoffRealPG(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-tenant-dns-handoff-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, runErr := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); runErr != nil {
		t.Fatalf("docker run: %v\n%s", runErr, output)
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
	schema, err := dbsql.Content.ReadFile("schema/quartermaster.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `SET TIME ZONE 'Pacific/Auckland'`); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	createdAt := time.Date(2026, time.September, 6, 12, 34, 56, 0, time.UTC)
	if err := quartermasterdb.New(conn).CreateTenantRecord(context.Background(), quartermasterdb.CreateTenantRecordParams{
		ID: "11111111-1111-4111-8111-111111111140", Name: "Timezone proof", CreatedAt: createdAt,
	}); err != nil {
		_ = conn.Close()
		t.Fatalf("create tenant in non-UTC database session: %v", err)
	}
	var observedAt time.Time
	if err := conn.QueryRowContext(context.Background(), `
		SELECT billing_entitlements_observed_at FROM quartermaster.tenants
		WHERE id = '11111111-1111-4111-8111-111111111140'
	`).Scan(&observedAt); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if !observedAt.Equal(createdAt) {
		_ = conn.Close()
		t.Fatalf("tenant observed-at shifted by session timezone: got %s, want %s", observedAt, createdAt)
	}
	if _, err := conn.ExecContext(context.Background(), `DELETE FROM quartermaster.tenants WHERE id = '11111111-1111-4111-8111-111111111140'`); err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO quartermaster.tenants (id, name, billing_entitlements_observed_at)
		VALUES
			('11111111-1111-4111-8111-111111111141', 'Subscribed', NOW()),
			('11111111-1111-4111-8111-111111111142', 'No subscription', NOW())
	`); err != nil {
		t.Fatal(err)
	}

	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	resp, err := server.CompleteTenantDNSEntitlementHandoff(serviceCtx(), &quartermasterpb.CompleteTenantDNSEntitlementHandoffRequest{SubscriptionCount: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.GetRecorded() {
		t.Fatal("receipt was not recorded")
	}
	var storedCount int64
	if err := db.QueryRowContext(context.Background(), `
		SELECT subscription_count FROM quartermaster.billing_entitlement_handoffs WHERE handoff_key = $1
	`, billingentitlements.TenantDNSHandoffKey).Scan(&storedCount); err != nil {
		t.Fatal(err)
	}
	if storedCount != 2 {
		t.Fatalf("stored census = %d, want 2", storedCount)
	}
	// The immutable receipt remains valid after a fail-closed, observed signup.
	if _, err := db.Exec(`INSERT INTO quartermaster.tenants (id, name, billing_entitlements_observed_at) VALUES ('11111111-1111-4111-8111-111111111143', 'Later signup', NOW())`); err != nil {
		t.Fatal(err)
	}
	resp, err = server.CompleteTenantDNSEntitlementHandoff(serviceCtx(), &quartermasterpb.CompleteTenantDNSEntitlementHandoffRequest{SubscriptionCount: 3})
	if err != nil || !resp.GetRecorded() {
		t.Fatalf("idempotent post-signup handoff = recorded %v, err %v", resp.GetRecorded(), err)
	}
	if err := db.QueryRowContext(context.Background(), `
		SELECT subscription_count FROM quartermaster.billing_entitlement_handoffs WHERE handoff_key = $1
	`, billingentitlements.TenantDNSHandoffKey).Scan(&storedCount); err != nil {
		t.Fatal(err)
	}
	if storedCount != 2 {
		t.Fatalf("immutable stored census changed after signup: %d", storedCount)
	}
}
