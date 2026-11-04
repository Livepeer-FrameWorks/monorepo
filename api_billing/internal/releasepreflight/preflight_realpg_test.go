//go:build schema_verify

package releasepreflight

import (
	"database/sql"
	"fmt"
	"os/exec"
	"reflect"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func TestReleasePreflightOldSchemaAndWorkerCandidates(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("Docker unavailable")
	}
	name := fmt.Sprintf("fw-purser-preflight-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	// The source fixture intentionally lacks the columns that expand introduces.
	ddl := `CREATE SCHEMA purser;
 CREATE TABLE purser.billing_tiers(id uuid PRIMARY KEY,base_price numeric,is_default_postpaid boolean);
 CREATE TABLE purser.tenant_subscriptions(id uuid PRIMARY KEY,tenant_id uuid,tier_id uuid,stripe_subscription_id text,mollie_subscription_id text);
 CREATE TABLE purser.payment_provider_intents(id uuid PRIMARY KEY,tenant_id uuid);
 CREATE TABLE purser.billing_collection_writeoffs(id uuid,tenant_id uuid,reason text);
 CREATE TABLE purser.billing_invoices(id uuid PRIMARY KEY,tenant_id uuid);
 CREATE TABLE purser.invoice_line_items(id uuid,tenant_id uuid,invoice_id uuid,pricing_source text);
 CREATE TABLE purser.subscription_operator_grants(subscription_id uuid);
 CREATE TABLE purser.provider_webhook_inbox(provider text,event_key text,headers jsonb,raw_payload bytea,status text DEFAULT 'pending',next_attempt_at timestamptz,processed_at timestamptz,attempts int,wait_attempts int,unresolved_since timestamptz,updated_at timestamptz,UNIQUE(provider,event_key));
 INSERT INTO purser.billing_tiers VALUES ('00000000-0000-4000-8000-000000000010',10,false);
 INSERT INTO purser.tenant_subscriptions VALUES
 ('00000000-0000-4000-8000-000000000011','00000000-0000-4000-8000-000000000001','00000000-0000-4000-8000-000000000010','sub-one',NULL),
 ('00000000-0000-4000-8000-000000000012','00000000-0000-4000-8000-000000000002','00000000-0000-4000-8000-000000000010','sub-two',NULL);
 INSERT INTO purser.payment_provider_intents VALUES ('00000000-0000-4000-8000-000000000021','00000000-0000-4000-8000-000000000001');
 INSERT INTO purser.billing_collection_writeoffs VALUES ('00000000-0000-4000-8000-000000000031','00000000-0000-4000-8000-000000000002','legacy');`
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		result := ""
		for _, table := range []string{"tenant_subscriptions", "payment_provider_intents", "billing_collection_writeoffs", "provider_webhook_inbox"} {
			var value string
			if err := db.QueryRow(`SELECT COALESCE(jsonb_agg(to_jsonb(r) ORDER BY to_jsonb(r)::text),'[]'::jsonb)::text FROM purser.` + table + ` r`).Scan(&value); err != nil {
				t.Fatal(err)
			}
			result += value
		}
		return result
	}
	before := snapshot()
	report, err := Run(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if after := snapshot(); before != after {
		t.Fatalf("preview changed source rows: before=%s after=%s", before, after)
	}
	byID := func(report *Report, id string) Check {
		for _, c := range report.Checks {
			if c.ID == id {
				return c
			}
		}
		t.Fatalf("missing %s", id)
		return Check{}
	}
	if c := byID(report, "unknown_submission_state"); c.Total != 1 || c.Rows[0].TenantID != "00000000-0000-4000-8000-000000000001" {
		t.Fatalf("unknown submission: %+v", c)
	}
	if c := byID(report, "collection_writeoff_constraint"); c.Total != 1 {
		t.Fatalf("constraint violation: %+v", c)
	}
	if _, err := db.Exec(`ALTER TABLE purser.tenant_subscriptions
 ADD COLUMN free_fallback_at timestamptz,ADD COLUMN free_fallback_moved_at timestamptz,ADD COLUMN free_fallback_attempted_at timestamptz,
 ADD COLUMN free_fallback_provider_stopping boolean NOT NULL DEFAULT false,ADD COLUMN free_fallback_provider_ended_at timestamptz,
 ADD COLUMN pending_tier_id uuid,ADD COLUMN pending_provider_tier_id uuid,ADD COLUMN pending_reason text,ADD COLUMN pending_effective_at timestamptz;
 CREATE TABLE purser.tenant_billing_closures(tenant_id uuid);
 UPDATE purser.tenant_subscriptions SET free_fallback_at=NOW()-INTERVAL '1 hour' WHERE tenant_id='00000000-0000-4000-8000-000000000001';
 UPDATE purser.tenant_subscriptions SET pending_tier_id=tier_id,pending_reason='downgrade',pending_effective_at=NOW() WHERE tenant_id='00000000-0000-4000-8000-000000000002';`); err != nil {
		t.Fatal(err)
	}
	before = snapshot()
	report, err = Run(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if after := snapshot(); before != after {
		t.Fatal("preview changed expanded rows")
	}
	expected := byID(report, "free_fallback_first_batch")
	actual, err := purserdb.New(db).ListDueFreeFallbacks(t.Context(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var predicted []string
	for _, r := range expected.Rows {
		predicted = append(predicted, r.TenantID)
	}
	if !reflect.DeepEqual(predicted, actual) {
		t.Fatalf("fallback preview=%v worker=%v", predicted, actual)
	}
	expected = byID(report, "provider_tier_sync")
	if err := purserdb.New(db).EnqueueUnconfirmedTierDowngrades(t.Context(), int64((72 * time.Hour).Seconds())); err != nil {
		t.Fatal(err)
	}
	var queued string
	if err := db.QueryRow(`SELECT headers->>'tenant_id' FROM purser.provider_webhook_inbox`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if expected.Total != 1 || queued != expected.Rows[0].TenantID {
		t.Fatalf("tier sync preview=%+v queued=%s", expected, queued)
	}
}
