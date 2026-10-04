//go:build stack_verify

package handlers

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"frameworks/api_billing/internal/appconfig"
	"frameworks/api_billing/internal/database/purserdb"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

// TestStackMonthEnd closes only the scenario's disposable tenant. Its real
// usage and issued documents stay at their actual timestamps. Expiring its
// last phase at the last complete metering window makes that tenant due now;
// no service clock or source-wide metering watermark changes.
func TestStackMonthEnd(t *testing.T) {
	tenant := os.Getenv("STACK_BILLING_TENANT_ID")
	statement := os.Getenv("STACK_BILLING_FIXTURE_STATEMENT_ID")
	if tenant == "" || statement == "" {
		t.Skip("run through stack scenario 27")
	}
	if _, err := uuid.Parse(tenant); err != nil {
		t.Fatal("invalid fixture tenant")
	}
	if _, err := uuid.Parse(statement); err != nil {
		t.Fatal("invalid fixture statement")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db, err := sql.Open("postgres", os.Getenv("STACK_BILLING_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err = db.PingContext(ctx); err != nil {
		t.Fatal("cannot connect to fixture database")
	}
	runtime, err := config.Load[appconfig.PurserRuntime](config.Options{Service: "purser"})
	if err != nil {
		t.Fatal(err)
	}
	previous := appconfig.InstallRuntime(config.NewLive(runtime, config.Options{Service: "purser"}))
	defer appconfig.InstallRuntime(previous)
	if runtime.WaiveUsageCharges || len(runtime.MissingSupplierIdentity()) != 0 {
		t.Fatal("the stack driver needs metered billing and the deployed supplier identity")
	}
	tls, err := config.Load[config.GRPCClientTLS](config.Options{Service: "purser"})
	if err != nil {
		t.Fatal(err)
	}
	addr := os.Getenv("STACK_BILLING_QM_ADDR")
	if addr == "" {
		addr = "quartermaster:19002"
	}
	logger := logging.NewLoggerWithService("stack-month-end")
	qm, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr: addr, Timeout: 10 * time.Second, Logger: logger,
		ServiceToken: os.Getenv("SERVICE_TOKEN"), AllowInsecure: tls.AllowInsecure,
		CACertFile: tls.CAPath, ServerName: os.Getenv("QUARTERMASTER_GRPC_TLS_SERVER_NAME"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer qm.Close()
	jobs := &JobManager{db: db, logger: logger, billing: NewService(db, logger, nil, qm, nil, nil, nil)}

	// The guard identifies the completed prepaid -> postpaid -> prepaid
	// round trip. Other tenants and their metering sources are untouched.
	var start, end time.Time
	err = database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
		if scanErr := tx.QueryRowContext(ctx, `
		SELECT s.billing_period_start,
		       date_bin(INTERVAL '5 minutes', NOW(), TIMESTAMPTZ '1970-01-01')
		FROM purser.tenant_subscriptions s
		WHERE s.tenant_id = $1 AND s.billing_model = 'prepaid' AND s.status = 'active'
		  AND EXISTS (SELECT 1 FROM purser.billing_invoices i WHERE i.tenant_id = s.tenant_id
		    AND i.id = $2 AND i.document_kind = 'prepaid_statement'
		    AND i.usage_details->'statement'->>'closes_prepaid_phase' = 'true')
		  AND EXISTS (SELECT 1 FROM purser.billing_invoices i WHERE i.tenant_id = s.tenant_id
		    AND i.document_kind = 'invoice' AND i.period_end = s.billing_period_start
		    AND i.usage_details->>'closes_postpaid_phase' = 'true')
		FOR UPDATE OF s`, tenant, statement).Scan(&start, &end); scanErr != nil {
			return fmt.Errorf("fixture is not the completed billing round trip: %w", scanErr)
		}
		if !end.After(start) {
			return fmt.Errorf("prepaid traffic must reach a complete metering window before month-end")
		}
		// Metering reports a window several minutes after it closes, so the
		// latest boundaries may not be reported yet; the period ends at the
		// latest boundary whose windows are all complete.
		completenessErr := jobs.assertMeteringComplete(ctx, tenant, start, end)
		for completenessErr != nil {
			previous := end.Add(-5 * time.Minute)
			if !previous.After(start) {
				return fmt.Errorf("real metering is incomplete: %w", completenessErr)
			}
			end = previous
			completenessErr = jobs.assertMeteringComplete(ctx, tenant, start, end)
		}
		_, updateErr := tx.ExecContext(ctx, `UPDATE purser.tenant_subscriptions
			SET billing_period_end = $2, next_billing_date = $2 WHERE tenant_id = $1`, tenant, end)
		return updateErr
	})
	if err != nil {
		t.Fatal(err)
	}
	// The daily invoice job runs at UTC midnight after this completed window.
	// Supply that tick's date directly to the same production finalizer.
	now := end.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	due, err := purserdb.New(stackTenantDueQueries{DBTX: db, tenant: tenant}).ListSubscriptionsDueForInvoice(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	var selected []purserdb.ListSubscriptionsDueForInvoiceRow
	for _, subscription := range due {
		if subscription.TenantID == tenant {
			selected = append(selected, subscription)
		}
	}
	if len(selected) != 1 {
		t.Fatalf("due test subscriptions = %d, want 1", len(selected))
	}
	if got := jobs.finalizeSubscriptionPeriods(ctx, selected, now, time.Time{}); got != 1 {
		t.Fatalf("month-end documents = %d, want 1", got)
	}
	if got := jobs.finalizeSubscriptionPeriods(ctx, selected, now, time.Time{}); got != 0 {
		t.Fatalf("repeating month-end produced %d additional documents", got)
	}
	t.Logf("closed real prepaid phase %s -> %s; repeated finalization produced no document", start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano))
}

// Restrict the production due-subscription query in SQL before it returns
// rows, so the driver cannot read or finalize another tenant's subscription.
type stackTenantDueQueries struct {
	purserdb.DBTX
	tenant string
}

func (q stackTenantDueQueries) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return q.DBTX.QueryContext(ctx, "SELECT * FROM ("+query+") AS due WHERE due.tenant_id = $2", append(args, q.tenant)...)
}
