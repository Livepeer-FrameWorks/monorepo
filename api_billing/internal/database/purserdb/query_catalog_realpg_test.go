//go:build schema_verify

package purserdb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

type generatedQuery struct {
	file string
	name string
	sql  string
}

func TestGeneratedQueryCatalogPrepares_RealPG(t *testing.T) {
	db := startQueryCatalogRealPG(t)
	preparePurserQueryCatalog(t, db)
	assertTenantAdmissionQueryExecution(t, db)
	assertTenantAdmissionQueryPlan(t, db)
	assertMediaAuthorityRefreshTriggers(t, db)

	ctx := context.Background()
	t.Run("x402 intent replay is explicit", func(t *testing.T) {
		queries := New(db)
		params := UpsertX402SettlementIntentParams{
			Network: "base", PayerAddress: "0x1111111111111111111111111111111111111111", Nonce: "1",
			TenantID: "10000000-0000-0000-0000-000000000001", AmountCents: 125,
			AuthPayload: json.RawMessage(`{"scheme":"exact"}`), ClientIp: "", QuoteID: "",
		}
		inserted, err := queries.UpsertX402SettlementIntent(ctx, params)
		if err != nil {
			t.Fatalf("insert settlement intent: %v", err)
		}
		if inserted.ID == "" || inserted.TenantID != params.TenantID || inserted.AmountCents != params.AmountCents {
			t.Fatalf("inserted settlement = %#v", inserted)
		}
		if _, err := queries.UpsertX402SettlementIntent(ctx, params); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("replayed insert error = %v, want sql.ErrNoRows", err)
		}
		existing, err := queries.GetX402SettlementByIdentity(ctx, GetX402SettlementByIdentityParams{
			Network: params.Network, PayerAddress: params.PayerAddress, Nonce: params.Nonce,
		})
		if err != nil {
			t.Fatalf("load replayed settlement intent: %v", err)
		}
		if existing.ID != inserted.ID || existing.AuthPayload == "" {
			t.Fatalf("existing settlement = %#v, inserted = %#v", existing, inserted)
		}
	})
}

func assertMediaAuthorityRefreshTriggers(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	const (
		tierID   = "92000000-0000-0000-0000-000000000001"
		tenantID = "92000000-0000-0000-0000-000000000002"
	)
	if _, err := db.ExecContext(ctx, `DELETE FROM purser.media_authority_refresh_outbox`); err != nil {
		t.Fatalf("isolate media-authority refresh test: %v", err)
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO purser.billing_tiers (id, tier_name, display_name, tier_level)
		  VALUES ($1, 'authority-trigger-realpg', 'Authority trigger', 2)`, []any{tierID}},
		{`INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model)
		  VALUES ($1, $2, 'active', 'prepaid')`, []any{tenantID, tierID}},
		{`INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency)
		  VALUES ($1, 100, 'EUR')`, []any{tenantID}},
		{`INSERT INTO purser.tier_entitlements (tier_id, key, value)
		  VALUES ($1, 'custom_subdomain_enabled', 'true'::jsonb)`, []any{tierID}},
		{`INSERT INTO purser.subscription_entitlement_overrides (subscription_id, key, value)
		  SELECT id, 'custom_domain_enabled', 'true'::jsonb
		  FROM purser.tenant_subscriptions WHERE tenant_id = $1`, []any{tenantID}},
		{`INSERT INTO purser.tier_pricing_rules (tier_id, meter, model, currency, included_quantity, unit_price)
		  VALUES ($1, 'delivered_minutes', 'all_usage', 'EUR', 0, 0.01)`, []any{tierID}},
		{`UPDATE purser.billing_tiers SET features = '{"processing_customizable":true}'::jsonb WHERE id = $1`, []any{tierID}},
		{`INSERT INTO purser.usage_records (
		     tenant_id, cluster_id, usage_type, unit, dimension_key, source_id,
		     report_id, usage_value, value_kind, period_start, period_end
		  ) VALUES (
		     $1, 'cluster-1', 'delivered_minutes', 'minutes', repeat('0', 64),
		     'trigger-test', 'report-1', 1, 'delta', NOW() - INTERVAL '5 minutes', NOW()
		  )`, []any{tenantID}},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed media-authority triggers: %v", err)
		}
	}
	rows, err := db.QueryContext(ctx, `
		SELECT reason FROM purser.media_authority_refresh_outbox
		WHERE tenant_id = $1 ORDER BY reason
	`, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	reasons := map[string]bool{}
	for rows.Next() {
		var reason string
		if err := rows.Scan(&reason); err != nil {
			t.Fatal(err)
		}
		reasons[reason] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, reason := range []string{
		"subscription_authority_changed",
		"prepaid_admission_gate_changed",
		"tier_entitlement_changed",
		"subscription_entitlement_changed",
		"tier_allowance_changed",
		"billing_tier_authority_changed",
		"allowance_usage_changed",
	} {
		if !reasons[reason] {
			t.Fatalf("real PostgreSQL trigger did not enqueue %q; got %v", reason, reasons)
		}
	}
	var subscriptionRevision int64
	if err := db.QueryRowContext(ctx, `
		SELECT revision FROM purser.media_authority_refresh_outbox
		WHERE tenant_id = $1 AND reason = 'subscription_authority_changed'
	`, tenantID).Scan(&subscriptionRevision); err != nil {
		t.Fatalf("read subscription authority revision: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions SET status = status WHERE tenant_id = $1
	`, tenantID); err != nil {
		t.Fatalf("write unchanged subscription state: %v", err)
	}
	var unchangedRevision int64
	if err := db.QueryRowContext(ctx, `
		SELECT revision FROM purser.media_authority_refresh_outbox
		WHERE tenant_id = $1 AND reason = 'subscription_authority_changed'
	`, tenantID).Scan(&unchangedRevision); err != nil {
		t.Fatalf("read unchanged subscription authority revision: %v", err)
	}
	if unchangedRevision != subscriptionRevision {
		t.Fatalf("no-op subscription update advanced authority revision: got %d want %d", unchangedRevision, subscriptionRevision)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions SET billing_period_start = NOW() WHERE tenant_id = $1
	`, tenantID); err != nil {
		t.Fatalf("write changed subscription state: %v", err)
	}
	var changedRevision int64
	if err := db.QueryRowContext(ctx, `
		SELECT revision FROM purser.media_authority_refresh_outbox
		WHERE tenant_id = $1 AND reason = 'subscription_authority_changed'
	`, tenantID).Scan(&changedRevision); err != nil {
		t.Fatalf("read changed subscription authority revision: %v", err)
	}
	if changedRevision != subscriptionRevision+1 {
		t.Fatalf("changed subscription update authority revision = %d, want %d", changedRevision, subscriptionRevision+1)
	}

	// A redelivered usage report upserts a row with its own values. It changes
	// no allowance and must not request a tenant authority refresh; a changed
	// value must.
	usageRevision := func() int64 {
		t.Helper()
		var revision int64
		if err := db.QueryRowContext(ctx, `
			SELECT revision FROM purser.media_authority_refresh_outbox
			WHERE tenant_id = $1 AND reason = 'allowance_usage_changed'
		`, tenantID).Scan(&revision); err != nil {
			t.Fatalf("read allowance usage revision: %v", err)
		}
		return revision
	}
	beforeUsage := usageRevision()
	if _, err := db.ExecContext(ctx, `
		UPDATE purser.usage_records SET usage_value = usage_value, updated_at = NOW()
		WHERE tenant_id = $1 AND usage_type = 'delivered_minutes'
	`, tenantID); err != nil {
		t.Fatalf("replay unchanged usage: %v", err)
	}
	if got := usageRevision(); got != beforeUsage {
		t.Fatalf("unchanged usage replay advanced authority revision: got %d want %d", got, beforeUsage)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE purser.usage_records SET usage_value = usage_value + 1
		WHERE tenant_id = $1 AND usage_type = 'delivered_minutes'
	`, tenantID); err != nil {
		t.Fatalf("write changed usage: %v", err)
	}
	if got := usageRevision(); got != beforeUsage+1 {
		t.Fatalf("changed usage authority revision = %d, want %d", got, beforeUsage+1)
	}

	if _, err := db.ExecContext(ctx, `
		UPDATE purser.media_authority_refresh_outbox
		SET pending_since = NOW() - INTERVAL '2 hours'
		WHERE tenant_id = $1 AND status <> 'completed'
	`, tenantID); err != nil {
		t.Fatalf("age refresh obligations: %v", err)
	}
	queries := New(db)
	claimed, err := queries.ClaimMediaAuthorityRefreshBatch(ctx, ClaimMediaAuthorityRefreshBatchParams{
		LeaseMs: int64(time.Minute / time.Millisecond), BatchSize: 1,
	})
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim aged refresh obligation: rows=%d err=%v", len(claimed), err)
	}
	claimedID, err := uuid.Parse(claimed[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := queries.FailMediaAuthorityRefresh(ctx, FailMediaAuthorityRefreshParams{
		NextAttemptAt: time.Now().Add(time.Minute),
		LastError:     sql.NullString{String: "permanent test failure", Valid: true},
		ID:            claimedID,
		Revision:      claimed[0].Revision,
	})
	if err != nil || changed != 1 {
		t.Fatalf("fail aged refresh obligation: changed=%d err=%v", changed, err)
	}
	if _, err := db.ExecContext(ctx, `SELECT purser.enqueue_media_authority_refresh($1::uuid, $2)`, tenantID, claimed[0].Reason); err != nil {
		t.Fatalf("supersede aged refresh obligation: %v", err)
	}
	var pendingSince time.Time
	var revision int64
	if err := db.QueryRowContext(ctx, `
		SELECT pending_since, revision FROM purser.media_authority_refresh_outbox WHERE id = $1
	`, claimedID).Scan(&pendingSince, &revision); err != nil {
		t.Fatalf("read retried obligation age origin: %v", err)
	}
	if revision != claimed[0].Revision+1 {
		t.Fatalf("superseding enqueue revision = %d, want %d", revision, claimed[0].Revision+1)
	}
	if time.Since(pendingSince) < 119*time.Minute {
		t.Fatalf("superseding enqueue reset pending_since: %s", pendingSince)
	}
	stats, err := queries.GetMediaAuthorityRefreshOutboxStats(ctx)
	if err != nil {
		t.Fatalf("read refresh outbox stats: %v", err)
	}
	if stats.OldestPendingSeconds < 119*time.Minute.Seconds() {
		t.Fatalf("oldest pending age reset by retry: got %.0fs", stats.OldestPendingSeconds)
	}
}

func TestGeneratedQueryCatalogPrepares_RealYugabyte(t *testing.T) {
	db := startQueryCatalogRealYugabyte(t)
	preparePurserQueryCatalog(t, db)
	assertTenantAdmissionQueryExecution(t, db)
	assertMediaAuthorityRefreshTriggers(t, db)
}

func assertTenantAdmissionQueryExecution(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	const (
		tierID           = "91000000-0000-0000-0000-000000000001"
		prepaidTenantID  = "91000000-0000-0000-0000-000000000002"
		postpaidTenantID = "91000000-0000-0000-0000-000000000003"
	)

	statements := []struct {
		query string
		args  []any
	}{
		{
			query: `INSERT INTO purser.billing_tiers (id, tier_name, display_name, tier_level)
			        VALUES ($1, 'admission-real-engine', 'Admission real-engine', 4)`,
			args: []any{tierID},
		},
		{
			query: `INSERT INTO purser.tenant_subscriptions
			        (tenant_id, tier_id, status, billing_model)
			        VALUES ($1, $2, 'active', 'prepaid')`,
			args: []any{prepaidTenantID, tierID},
		},
		{
			query: `INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency)
			        VALUES ($1, 100, 'EUR')`,
			args: []any{prepaidTenantID},
		},
		{
			query: `INSERT INTO purser.usage_reservations
			        (tenant_id, source_id, cluster_id, sequence, report_id, period_start,
			         period_end, meters, reserved_amount_micro, currency, updated_at)
			        VALUES
			        ($1, 'active', 'cluster-1', 1, 'active-report', NOW() - INTERVAL '1 minute',
			         NOW(), '{}'::jsonb, 1250001, 'EUR', NOW()),
			        ($1, 'stale', 'cluster-1', 1, 'stale-report', NOW() - INTERVAL '11 minutes',
			         NOW() - INTERVAL '10 minutes', '{}'::jsonb, 990000, 'EUR', NOW() - INTERVAL '10 minutes')`,
			args: []any{prepaidTenantID},
		},
		{
			query: `INSERT INTO purser.tenant_subscriptions
			        (tenant_id, tier_id, status, billing_model, payment_method, stripe_subscription_id)
			        VALUES ($1, $2, 'suspended', 'postpaid', 'stripe', 'sub_real_engine')`,
			args: []any{postpaidTenantID, tierID},
		},
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
			t.Fatalf("seed tenant admission query: %v\n%s", err, statement.query)
		}
	}

	queries := New(db)
	prepaid, err := queries.GetTenantAdmissionStatus(ctx, GetTenantAdmissionStatusParams{
		TenantID: prepaidTenantID,
		Currency: "EUR",
	})
	if err != nil {
		t.Fatalf("execute prepaid tenant admission query: %v", err)
	}
	if !prepaid.BalanceCents.Valid || prepaid.BalanceCents.Int64 != 100 {
		t.Fatalf("prepaid balance = %#v, want valid 100", prepaid.BalanceCents)
	}
	if prepaid.ReservedBalanceCents != 126 {
		t.Fatalf("reserved balance = %d, want ceil(1250001/10000) = 126", prepaid.ReservedBalanceCents)
	}
	if prepaid.BillingModel != "prepaid" || prepaid.SubscriptionStatus != "active" || prepaid.PaymentMethod.Valid {
		t.Fatalf("unexpected prepaid admission row: %#v", prepaid)
	}
	if prepaid.TierLevel != 4 {
		t.Fatalf("prepaid tier level = %d, want 4", prepaid.TierLevel)
	}

	postpaid, err := queries.GetTenantAdmissionStatus(ctx, GetTenantAdmissionStatusParams{
		TenantID: postpaidTenantID,
		Currency: "EUR",
	})
	if err != nil {
		t.Fatalf("execute postpaid tenant admission query: %v", err)
	}
	if postpaid.BalanceCents.Valid || postpaid.ReservedBalanceCents != 0 {
		t.Fatalf("postpaid nullable balance/reservation = %#v/%d, want NULL/0", postpaid.BalanceCents, postpaid.ReservedBalanceCents)
	}
	if postpaid.SubscriptionStatus != "suspended" || postpaid.PaymentMethod.String != "stripe" || postpaid.StripeSubscriptionID.String != "sub_real_engine" {
		t.Fatalf("unexpected postpaid admission row: %#v", postpaid)
	}
}

func assertTenantAdmissionQueryPlan(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const tenantID = "91000000-0000-0000-0000-000000000002"

	_, err := db.ExecContext(ctx, `
		INSERT INTO purser.usage_reservations (
			tenant_id, source_id, cluster_id, sequence, report_id, period_start,
			period_end, meters, reserved_amount_micro, currency, updated_at
		)
		SELECT $1::uuid, 'stale-source-' || n, 'stale-cluster-' || n, 1,
		       'stale-report-' || n, NOW() - INTERVAL '11 minutes',
		       NOW() - INTERVAL '10 minutes', '{}'::jsonb, 10000, 'EUR',
		       NOW() - INTERVAL '10 minutes'
		FROM generate_series(1, 5000) AS n`, tenantID)
	if err != nil {
		t.Fatalf("seed realistic reservation volume: %v", err)
	}
	if _, err := db.ExecContext(ctx, `ANALYZE purser.usage_reservations`); err != nil {
		t.Fatalf("analyze reservations: %v", err)
	}

	rows, err := db.QueryContext(ctx, `
		EXPLAIN (ANALYZE, BUFFERS, FORMAT TEXT)
		SELECT COALESCE(SUM(reserved_amount_micro), 0)
		FROM purser.usage_reservations
		WHERE tenant_id = $1::uuid
		  AND currency = 'EUR'
		  AND updated_at >= NOW() - INTERVAL '3 minutes'`, tenantID)
	if err != nil {
		t.Fatalf("explain admission reservation lookup: %v", err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatalf("scan explain plan: %v", err)
		}
		plan.WriteString(line)
		plan.WriteByte('\n')
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read explain plan: %v", err)
	}
	if !strings.Contains(plan.String(), "idx_usage_reservations_tenant_currency_recent") {
		t.Fatalf("admission reservation lookup did not use bounded recent index:\n%s", plan.String())
	}
}

func preparePurserQueryCatalog(t *testing.T, db *sql.DB) {
	t.Helper()
	queries := loadGeneratedQueries(t)
	if len(queries) < 100 {
		t.Fatalf("found only %d generated queries; catalog discovery is incomplete", len(queries))
	}

	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	for index, query := range queries {
		preparedName := fmt.Sprintf("purser_contract_%d", index)
		if _, err := conn.ExecContext(ctx, "PREPARE "+preparedName+" AS "+query.sql); err != nil {
			t.Fatalf("prepare %s from %s: %v\n%s", query.name, query.file, err, query.sql)
		}
		if _, err := conn.ExecContext(ctx, "DEALLOCATE "+preparedName); err != nil {
			t.Fatalf("deallocate %s from %s: %v", query.name, query.file, err)
		}
	}

}

func loadGeneratedQueries(t *testing.T) []generatedQuery {
	t.Helper()
	paths, err := filepath.Glob("*.sql.go")
	if err != nil {
		t.Fatal(err)
	}
	queries := make([]generatedQuery, 0, len(paths))
	for _, path := range paths {
		file, parseErr := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", path, parseErr)
		}
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, specification := range general.Specs {
				value, ok := specification.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for valueIndex, expression := range value.Values {
					literal, ok := expression.(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						continue
					}
					querySQL, unquoteErr := strconv.Unquote(literal.Value)
					if unquoteErr != nil {
						t.Fatalf("unquote constant in %s: %v", path, unquoteErr)
					}
					if !strings.HasPrefix(querySQL, "-- name:") {
						continue
					}
					name := "unknown"
					if valueIndex < len(value.Names) {
						name = value.Names[valueIndex].Name
					}
					queries = append(queries, generatedQuery{file: path, name: name, sql: querySQL})
				}
			}
		}
	}
	return queries
}

func startQueryCatalogRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-purser-query-catalog-realpg-%d", time.Now().UnixNano())
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

func startQueryCatalogRealYugabyte(t *testing.T) *sql.DB {
	t.Helper()
	if db, ok := dockerpg.OpenSharedYugabyteDatabase(t, "purser"); ok {
		schema, err := dbsql.Content.ReadFile("schema/purser.sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(schema)); err != nil {
			t.Fatal(err)
		}
		return db
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-purser-query-catalog-yb-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.YugabyteImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "--hostname", name, image, "bash", "-c", `exec bin/yugabyted start --background=false --advertise_address="$(hostname -i)" --tserver_flags=yb_enable_read_committed_isolation=true`); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5433/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://yugabyte@127.0.0.1:%s/yugabyte?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReadyFor(db, name, 3*time.Minute); err != nil {
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
