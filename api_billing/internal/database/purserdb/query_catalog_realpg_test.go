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

func TestGeneratedQueryCatalogPrepares_RealYugabyte(t *testing.T) {
	db := startQueryCatalogRealYugabyte(t)
	preparePurserQueryCatalog(t, db)
	assertTenantAdmissionQueryExecution(t, db)
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
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "--hostname", name, image, "bash", "-c", `exec bin/yugabyted start --background=false --advertise_address="$(hostname -i)"`); err != nil {
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
