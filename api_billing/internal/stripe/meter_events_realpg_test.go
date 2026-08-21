//go:build schema_verify

package stripe

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"sync"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
	stripeapi "github.com/stripe/stripe-go/v85"
)

func startStripeMeterRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-stripe-meter-realpg-%d", time.Now().UnixNano())
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

func TestStripeMeterEventRepository_RealPG(t *testing.T) {
	db := startStripeMeterRealPG(t)
	ctx := context.Background()
	tenantID := uuid.MustParse("31000000-0000-4000-8000-000000000001")
	tierID := uuid.MustParse("32000000-0000-4000-8000-000000000001")
	invoiceID := uuid.MustParse("33000000-0000-4000-8000-000000000001")
	periodStart := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	periodEnd := periodStart.AddDate(0, 1, 0)

	seedTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	seedStatements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO purser.billing_tiers (id, tier_name, display_name)
		  VALUES ($1, 'meter-contract', 'Meter Contract')`, []any{tierID}},
		{`INSERT INTO purser.tenant_subscriptions
			(id, tenant_id, tier_id, status, stripe_customer_id)
		  VALUES (gen_random_uuid(), $1, $2, 'active', 'cus_contract')`, []any{tenantID, tierID}},
		{`INSERT INTO purser.cluster_pricing
			(id, cluster_id, pricing_model, metered_rates)
		  VALUES (gen_random_uuid(), 'operator-contract', 'metered',
			'{"transcode_rendition_seconds":{"stripe_meter_event_name":"meter.transcode"}}'::jsonb)`, nil},
		{`INSERT INTO purser.billing_invoices
			(id, tenant_id, status, period_start, period_end, due_date)
		  VALUES ($1, $2, 'pending', $3, $4, $4)`, []any{invoiceID, tenantID, periodStart, periodEnd}},
		{`INSERT INTO purser.invoice_line_items
			(id, invoice_id, tenant_id, line_key, meter, unit, dimensions, description,
			 quantity, billable_quantity, unit_price, amount, currency, cluster_id, pricing_source)
		  VALUES
			(gen_random_uuid(), $1, $2, 'transcode-h264', 'transcode_rendition_seconds', 'second',
			 '{"output_codec":"h264"}', 'H.264 transcode', 3600, 3600, 0.01, 36, 'EUR',
			 'operator-contract', 'cluster_metered'),
			(gen_random_uuid(), $1, $2, 'transcode-av1', 'transcode_rendition_seconds', 'second',
			 '{"output_codec":"av1"}', 'AV1 transcode', 900, 900, 0.01, 9, 'EUR',
			 'operator-contract', 'cluster_metered')`, []any{invoiceID, tenantID}},
	}
	for _, statement := range seedStatements {
		if _, err := seedTx.ExecContext(ctx, statement.query, statement.args...); err != nil {
			_ = seedTx.Rollback()
			t.Fatalf("seed Stripe meter contract: %v", err)
		}
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := EnqueueMeterEvents(ctx, tx, invoiceID.String(), tenantID.String(), "pending"); err != nil {
			_ = tx.Rollback()
			t.Fatalf("enqueue: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}

	var queued int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM purser.stripe_meter_events_outbox`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 2 {
		t.Fatalf("queued rows = %d, want 2 distinct dimension buckets after idempotent replay", queued)
	}

	var mu sync.Mutex
	payloads := make(map[string]map[string]string)
	flusher := NewMeterFlusher(db)
	flusher.SendMeterEvent = func(_ context.Context, params *stripeapi.BillingMeterEventParams) error {
		if _, err := uuid.Parse(*params.Identifier); err != nil {
			return fmt.Errorf("identifier is not UUID: %w", err)
		}
		if *params.EventName != "meter.transcode" || params.Payload["stripe_customer_id"] != "cus_contract" {
			return fmt.Errorf("unexpected Stripe payload: event=%q payload=%v", *params.EventName, params.Payload)
		}
		mu.Lock()
		payloads[params.Payload["dimension_output_codec"]] = params.Payload
		mu.Unlock()
		return nil
	}
	sent, deferred, err := flusher.Flush(ctx)
	if err != nil {
		t.Fatalf("flush: %v", err)
	}
	if sent != 2 || deferred != 0 || len(payloads) != 2 || payloads["h264"] == nil || payloads["av1"] == nil {
		t.Fatalf("flush sent=%d deferred=%d payloads=%v", sent, deferred, payloads)
	}

	var marked int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM purser.stripe_meter_events_outbox
		WHERE sent_at IS NOT NULL AND attempt_count = 0 AND last_error IS NULL
	`).Scan(&marked); err != nil {
		t.Fatal(err)
	}
	if marked != 2 {
		t.Fatalf("marked rows = %d, want 2", marked)
	}
}
