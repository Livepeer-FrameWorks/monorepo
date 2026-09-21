//go:build schema_verify

package datamigrations

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/fx"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

const (
	currencyISOContractMigrationPath = "migrations/purser/v0.3.7/contract/001_prepaid_balance_currency_iso.sql"
	contractMigrationPath            = "migrations/purser/v0.3.11/contract/001_eur_ledger_constraints.sql"
	requiredFXContractMigrationPath  = "migrations/purser/v0.3.11/contract/002_fx_fields_required.sql"
)

// startPreContractPurserRealPG applies the current baseline and removes the
// constraints the v0.3.11 contract phase adds, which is the schema the data
// migration runs against on an upgraded cluster. The v0.3.7 uppercase ISO
// CHECK on prepaid balances stays; a USD balance row satisfies it.
func startPreContractPurserRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-purser-eur-ledger-realpg-%d", time.Now().UnixNano())
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
		t.Fatalf("apply purser schema: %v", err)
	}
	if _, err := db.Exec(`
ALTER TABLE purser.prepaid_balances DROP CONSTRAINT chk_prepaid_balances_ledger_currency;
ALTER TABLE purser.billing_invoices DROP CONSTRAINT chk_billing_invoices_ledger_currency;
ALTER TABLE purser.pending_topups
    ALTER COLUMN original_amount_cents DROP NOT NULL, ALTER COLUMN original_currency DROP NOT NULL,
    ALTER COLUMN eur_amount_cents DROP NOT NULL, ALTER COLUMN fx_units_per_eur DROP NOT NULL,
    ALTER COLUMN fx_source DROP NOT NULL, ALTER COLUMN fx_reference_date DROP NOT NULL;
ALTER TABLE purser.x402_payment_quotes
    ALTER COLUMN original_amount_cents DROP NOT NULL, ALTER COLUMN original_currency DROP NOT NULL,
    ALTER COLUMN eur_amount_cents DROP NOT NULL, ALTER COLUMN fx_units_per_eur DROP NOT NULL,
    ALTER COLUMN fx_source DROP NOT NULL, ALTER COLUMN fx_reference_date DROP NOT NULL;
ALTER TABLE purser.payment_reversals
    ALTER COLUMN original_amount_cents DROP NOT NULL, ALTER COLUMN original_currency DROP NOT NULL,
    ALTER COLUMN eur_amount_cents DROP NOT NULL, ALTER COLUMN fx_units_per_eur DROP NOT NULL,
    ALTER COLUMN fx_source DROP NOT NULL, ALTER COLUMN fx_reference_date DROP NOT NULL;
ALTER TABLE purser.billing_payments
    ALTER COLUMN original_amount_cents DROP NOT NULL, ALTER COLUMN original_currency DROP NOT NULL,
    ALTER COLUMN eur_amount_cents DROP NOT NULL, ALTER COLUMN fx_units_per_eur DROP NOT NULL,
    ALTER COLUMN fx_source DROP NOT NULL, ALTER COLUMN fx_reference_date DROP NOT NULL;
ALTER TABLE purser.crypto_wallets DROP CONSTRAINT chk_crypto_wallets_prepaid_fx;
ALTER TABLE purser.simplified_invoices
    ALTER COLUMN net_eur_cents DROP NOT NULL, ALTER COLUMN vat_eur_cents DROP NOT NULL,
    ALTER COLUMN fx_units_per_eur DROP NOT NULL, ALTER COLUMN fx_reference_date DROP NOT NULL;
ALTER TABLE purser.crypto_invoices
    ALTER COLUMN net_eur_cents DROP NOT NULL, ALTER COLUMN vat_eur_cents DROP NOT NULL,
    ALTER COLUMN fx_units_per_eur DROP NOT NULL, ALTER COLUMN fx_reference_date DROP NOT NULL;
ALTER TABLE purser.crypto_wallets ADD COLUMN quoted_usd_to_eur_rate NUMERIC(12,8);
ALTER TABLE purser.x402_payment_quotes ADD COLUMN eur_per_usd_rate NUMERIC(20, 10);
ALTER TABLE purser.simplified_invoices ADD COLUMN ecb_rate DECIMAL(10,6);
ALTER TABLE purser.crypto_invoices ADD COLUMN ecb_rate DECIMAL(10,6);`); err != nil {
		t.Fatalf("remove contract constraints: %v", err)
	}
	return db
}

type ledgerFixture struct {
	t  *testing.T
	db *sql.DB
}

func (f ledgerFixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(query, args...); err != nil {
		f.t.Fatalf("%s: %v", strings.Fields(query)[0:3], err)
	}
}

func (f ledgerFixture) tenant(tenantID string) {
	f.t.Helper()
	tierID := uuid.NewString()
	f.exec(`INSERT INTO purser.billing_tiers (id, tier_name, display_name, currency) VALUES ($1, $2, 'Ledger tier', 'EUR')`, tierID, "ledger-"+tierID)
	f.exec(`INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model) VALUES ($1, $2, 'active', 'prepaid')`, tenantID, tierID)
}

func (f ledgerFixture) creditedTopup(tenantID, currency string, amount int64, at time.Time) string {
	f.t.Helper()
	topupID := uuid.NewString()
	f.exec(`INSERT INTO purser.pending_topups (id, tenant_id, provider, amount_cents, currency, status, expires_at, completed_at, created_at)
VALUES ($1, $2, 'stripe', $3, $4, 'completed', $5, $5, $5)`, topupID, tenantID, amount, currency, at)
	f.exec(`INSERT INTO purser.balance_transactions (tenant_id, amount_cents, balance_after_cents, transaction_type, reference_id, reference_type, created_at)
VALUES ($1, $2, $2, 'topup', $3, 'topup', $4)`, tenantID, amount, topupID, at)
	return topupID
}

func (f ledgerFixture) count(query string, args ...any) int64 {
	f.t.Helper()
	var n int64
	if err := f.db.QueryRow(query, args...).Scan(&n); err != nil {
		f.t.Fatalf("count: %v", err)
	}
	return n
}

func admissionEUR(t *testing.T, db *sql.DB, tenantID string) int64 {
	t.Helper()
	row, err := purserdb.New(db).GetTenantAdmissionStatus(context.Background(), purserdb.GetTenantAdmissionStatusParams{TenantID: tenantID, Currency: "EUR"})
	if err != nil {
		t.Fatalf("admission status: %v", err)
	}
	return row.BalanceCents.Int64
}

func TestEURLedgerConversionDataMigration_RealPG(t *testing.T) {
	db := startPreContractPurserRealPG(t)
	ctx := context.Background()
	f := ledgerFixture{t: t, db: db}
	historyFetches := 0
	fetch := func(_ context.Context, feed fx.Feed) ([]byte, error) {
		if feed != fx.FeedHistory {
			return nil, fmt.Errorf("unexpected feed %s", feed)
		}
		historyFetches++
		return os.ReadFile(filepath.Join("testdata", "eurofxref-hist.xml"))
	}
	Register(Settings{Fetch: fetch})
	openDB := func() (*sql.DB, error) { return db, nil }
	runCLI := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := datamigrate.HandleArgv(ctx, openDB, &out, append([]string{"data-migrations"}, args...))
		return out.String(), err
	}

	// Tenant A: a USD card top-up stranded in a USD row, partly refunded, and an
	// EUR card top-up held in a lowercase "eur" row.
	tenantA := "00000000-0000-4000-8000-00000000000a"
	f.tenant(tenantA)
	usdCredit := time.Date(2026, 3, 31, 14, 0, 0, 0, time.UTC)
	usdTopup := f.creditedTopup(tenantA, "USD", 2500, usdCredit)
	reversalID := uuid.NewString()
	f.exec(`INSERT INTO purser.payment_reversals (id, tenant_id, pending_topup_id, provider, reversal_type, provider_reversal_id, amount_cents, currency, status, created_at)
VALUES ($1, $2, $3, 'stripe', 'refund', 're_usd', 500, 'USD', 'succeeded', $4)`, reversalID, tenantA, usdTopup, time.Date(2026, 4, 7, 9, 0, 0, 0, time.UTC))
	f.exec(`INSERT INTO purser.balance_transactions (tenant_id, amount_cents, balance_after_cents, transaction_type, reference_id, reference_type, created_at)
VALUES ($1, -500, 2000, 'refund', $2, 'payment_reversal', $3)`, tenantA, reversalID, time.Date(2026, 4, 7, 9, 0, 0, 0, time.UTC))
	f.exec(`INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency) VALUES ($1, 2000, 'USD')`, tenantA)
	eurTopup := f.creditedTopup(tenantA, "EUR", 1000, time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC))
	// Contract phases are deferred and run together after this data migration,
	// so a cluster that has not applied the v0.3.7 contract has no ISO CHECK
	// and can hold a lowercase row written after the v0.3.7 postdeploy merge.
	f.exec(`ALTER TABLE purser.prepaid_balances DROP CONSTRAINT chk_prepaid_balances_currency_iso`)
	f.exec(`INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, balance_remainder_micro, currency) VALUES ($1, 1000, 2500, 'eur')`, tenantA)
	f.exec(`INSERT INTO purser.usage_reservations (tenant_id, source_id, cluster_id, sequence, report_id, period_start, period_end, meters, reserved_amount_micro, currency)
VALUES ($1, 'src', 'cluster-a', 1, 'report-a', NOW(), NOW(), '{}', 0, 'eur')`, tenantA)

	// Tenant B: a USD balance larger than the credits that explain it.
	tenantB := "ffffffff-0000-4000-8000-00000000000b"
	f.tenant(tenantB)
	f.creditedTopup(tenantB, "USD", 500, time.Date(2026, 2, 19, 11, 0, 0, 0, time.UTC))
	f.exec(`INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency) VALUES ($1, 999, 'USD')`, tenantB)

	// Red: admission reads the EUR ledger, which holds nothing for tenant A.
	if got := admissionEUR(t, db, tenantA); got != 0 {
		t.Fatalf("admission EUR before migration = %d, want 0", got)
	}
	currencyISOContract, err := dbsql.Content.ReadFile(currencyISOContractMigrationPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(currencyISOContract)); err == nil {
		t.Fatal("v0.3.7 currency contract applied while a lowercase balance row exists")
	}
	contract, err := dbsql.Content.ReadFile(contractMigrationPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(contract)); err == nil {
		t.Fatal("contract EUR ledger constraints applied while non-EUR balance rows exist")
	}
	requiredFXContract, err := dbsql.Content.ReadFile(requiredFXContractMigrationPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(requiredFXContract)); err == nil {
		t.Fatal("contract required FX fields applied while money rows lack FX fields")
	}

	snapshot := func() string {
		return fmt.Sprintf("balances=%d transactions=%d rates=%d fx=%d",
			f.count(`SELECT COUNT(*) FROM purser.prepaid_balances`),
			f.count(`SELECT COUNT(*) FROM purser.balance_transactions`),
			f.count(`SELECT COUNT(*) FROM purser.fx_rates`),
			f.count(`SELECT COUNT(*) FROM purser.pending_topups WHERE fx_source IS NOT NULL`))
	}
	before := snapshot()
	output, err := runCLI("run", EURLedgerConversionID, "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v\n%s", err, output)
	}
	if !strings.Contains(output, "tenant="+tenantA+" dry-run: scanned=") || !strings.Contains(output, "tenant="+tenantB+" dry-run:") {
		t.Fatalf("dry run output does not report both tenants:\n%s", output)
	}
	if !strings.Contains(output, "errors=1") {
		t.Fatalf("dry run did not report tenant B's residual:\n%s", output)
	}
	if after := snapshot(); after != before || historyFetches != 0 {
		t.Fatalf("dry run wrote or fetched: before %s, after %s, history fetches %d", before, after, historyFetches)
	}

	output, err = runCLI("run", EURLedgerConversionID)
	if err == nil {
		t.Fatalf("run with an untraceable residual succeeded:\n%s", output)
	}
	if !strings.Contains(err.Error(), tenantB) || !strings.Contains(err.Error(), "residual of 499 cents") {
		t.Fatalf("residual error does not name tenant and residual: %v", err)
	}
	if got := f.count(`SELECT COUNT(*) FROM purser.prepaid_balances WHERE tenant_id = $1 AND currency = 'USD' AND balance_cents = 999`, tenantB); got != 1 {
		t.Fatal("failed tenant scope changed its balance")
	}

	// Green for tenant A: 2500 USD at 1.0811 is 2312 EUR cents, the 500 USD
	// refund of that top-up converts at the same rate to -462, and the "eur"
	// row folds at rate 1 with its sub-cent remainder.
	if got := admissionEUR(t, db, tenantA); got != 2312-462+1000 {
		t.Fatalf("admission EUR after migration = %d, want 2850", got)
	}
	if got := f.count(`SELECT balance_remainder_micro FROM purser.prepaid_balances WHERE tenant_id = $1 AND currency = 'EUR'`, tenantA); got != 2500 {
		t.Fatalf("EUR remainder = %d, want 2500", got)
	}
	if got := f.count(`SELECT COUNT(*) FROM purser.prepaid_balances WHERE tenant_id = $1 AND currency <> 'EUR'`, tenantA); got != 0 {
		t.Fatalf("tenant A non-EUR rows = %d", got)
	}
	if got := f.count(`SELECT COUNT(*) FROM purser.usage_reservations WHERE tenant_id = $1 AND currency = 'EUR'`, tenantA); got != 1 {
		t.Fatal("lowercase eur reservation was not folded")
	}
	var conversionAmounts []string
	rows, err := db.Query(`SELECT reference_type, amount_cents, balance_after_cents FROM purser.balance_transactions
WHERE tenant_id = $1 AND transaction_type = 'fx_conversion' ORDER BY created_at, amount_cents`, tenantA)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var referenceType string
		var amount, after int64
		if err := rows.Scan(&referenceType, &amount, &after); err != nil {
			t.Fatal(err)
		}
		conversionAmounts = append(conversionAmounts, fmt.Sprintf("%s:%d", referenceType, amount))
	}
	_ = rows.Close()
	if got := strings.Join(conversionAmounts, ","); !strings.Contains(got, "fx_conversion:2312") || !strings.Contains(got, "fx_conversion:-462") || !strings.Contains(got, "fx_currency_fold:1000") || len(conversionAmounts) != 3 {
		t.Fatalf("conversion transactions = %s", got)
	}
	var original, eur int64
	var currency, source, units, referenceDate string
	if err := db.QueryRow(`SELECT original_amount_cents, original_currency, eur_amount_cents, fx_units_per_eur::text, fx_source, fx_reference_date::text
FROM purser.pending_topups WHERE id = $1`, usdTopup).Scan(&original, &currency, &eur, &units, &source, &referenceDate); err != nil {
		t.Fatal(err)
	}
	if original != 2500 || currency != "USD" || eur != 2312 || units != "1.0811000000" || source != "ecb" || referenceDate != "2026-03-31" {
		t.Fatalf("USD top-up FX = %d %s %d %s %s %s", original, currency, eur, units, source, referenceDate)
	}
	if err := db.QueryRow(`SELECT eur_amount_cents, fx_reference_date::text FROM purser.payment_reversals WHERE id = $1`, reversalID).Scan(&eur, &referenceDate); err != nil {
		t.Fatal(err)
	}
	if eur != 462 || referenceDate != "2026-03-31" {
		t.Fatalf("reversal FX eur=%d date=%s, want 462 at the top-up's 2026-03-31 rate", eur, referenceDate)
	}
	if err := db.QueryRow(`SELECT original_currency, fx_source, eur_amount_cents FROM purser.pending_topups WHERE id = $1`, eurTopup).Scan(&currency, &source, &eur); err != nil {
		t.Fatal(err)
	}
	if currency != "EUR" || source != "identity" || eur != 1000 {
		t.Fatalf("EUR top-up FX = %s %s %d", currency, source, eur)
	}
	if historyFetches != 1 {
		t.Fatalf("history fetches = %d, want one fetch filling the missing historical rates", historyFetches)
	}
	if got := f.count(`SELECT COUNT(*) FROM purser.fx_rates WHERE currency = 'USD' AND reference_date = DATE '2026-03-31'`); got != 1 {
		t.Fatal("historical USD rate was not stored")
	}

	// Idempotent: rerunning tenant A's scope changes nothing.
	transactionsBefore := f.count(`SELECT COUNT(*) FROM purser.balance_transactions WHERE tenant_id = $1`, tenantA)
	migration := datamigrate.Lookup(EURLedgerConversionID)
	progress, err := migration.Run(ctx, db, datamigrate.RunOptions{Scope: datamigrate.ScopeKey{Kind: "tenant", Value: tenantA}})
	if err != nil || progress.Changed != 0 {
		t.Fatalf("rerun progress=%+v err=%v, want no changes", progress, err)
	}
	if got := f.count(`SELECT COUNT(*) FROM purser.balance_transactions WHERE tenant_id = $1`, tenantA); got != transactionsBefore {
		t.Fatalf("rerun wrote transactions: %d -> %d", transactionsBefore, got)
	}
	if got := admissionEUR(t, db, tenantA); got != 2850 {
		t.Fatalf("admission EUR after rerun = %d", got)
	}

	if _, err := runCLI("verify", EURLedgerConversionID); err == nil {
		t.Fatal("verify passed while tenant B still holds a USD balance row")
	}

	// Tenant B's balance is corrected to the credits that explain it; the
	// migration then converts it and the job completes.
	f.exec(`UPDATE purser.prepaid_balances SET balance_cents = 500 WHERE tenant_id = $1`, tenantB)
	if output, err := runCLI("run", EURLedgerConversionID); err != nil {
		t.Fatalf("run after correction: %v\n%s", err, output)
	}
	// 500 USD cents at the 2026-02-19 rate of 1.0441 is 478.88 EUR cents.
	if got := admissionEUR(t, db, tenantB); got != 479 {
		t.Fatalf("tenant B admission EUR = %d, want 479", got)
	}
	if output, err := runCLI("verify", EURLedgerConversionID); err != nil {
		t.Fatalf("verify: %v\n%s", err, output)
	}
	if err := VerifyEURLedgerConversion(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(currencyISOContract)); err != nil {
		t.Fatalf("v0.3.7 currency contract after migration: %v", err)
	}
	if _, err := db.Exec(string(contract)); err != nil {
		t.Fatalf("contract after migration: %v", err)
	}
	if _, err := db.Exec(string(requiredFXContract)); err != nil {
		t.Fatalf("required FX contract after migration: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO purser.pending_topups (tenant_id, provider, amount_cents, currency, expires_at) VALUES ($1, 'stripe', 100, 'EUR', NOW())`, uuid.NewString()); err == nil {
		t.Fatal("top-up without FX fields accepted after contract")
	}
	if _, err := db.Exec(`INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency) VALUES ($1, 1, 'eur')`, uuid.NewString()); err == nil {
		t.Fatal("lowercase eur balance row accepted after contract")
	}
}
