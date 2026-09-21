// Package datamigrations holds Purser's service-owned background data
// migrations.
package datamigrations

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/fx"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/google/uuid"
)

const EURLedgerConversionID = "purser_eur_ledger_conversion_v0_3_11"

// Balance transaction reference types this migration writes. Each is unique
// per tenant and referenced row, which makes reruns insert nothing twice.
const (
	// fxConversionReferenceType references the original non-EUR balance
	// transaction that a conversion transaction carries into the EUR row.
	fxConversionReferenceType = "fx_conversion"
	// fxFoldReferenceType references the non-canonical EUR spelling balance
	// row folded into the EUR row at rate 1.
	fxFoldReferenceType     = "fx_currency_fold"
	fxConversionTransaction = "fx_conversion"
	// microPerCent matches the prepaid ledger: balance_remainder_micro holds
	// micro currency units (10^-6) in [0, microPerCent).
	microPerCent = int64(10_000)
)

// Settings configures the EUR ledger conversion.
type Settings struct {
	// Fetch retrieves ECB feeds when a historical rate is not stored yet.
	Fetch fx.Fetcher
}

// Register adds Purser's data migrations to the datamigrate registry.
func Register(settings Settings) {
	datamigrate.Register(EURLedgerConversion(settings))
}

// EURLedgerConversion converts every non-EUR prepaid balance row into the
// tenant's EUR balance and records FX fields on the money rows that lack them.
//
// Per tenant it traces each non-EUR row's balance to the top-up, crypto wallet,
// and reversal transactions that moved it, converts each of those at the ECB
// rate of the original credit's date, writes one fx_conversion transaction per
// original transaction, and deletes the non-EUR row. A row whose balance is
// not fully explained by those movements fails the tenant scope. Non-canonical
// spellings of EUR, such as a lowercase eur, fold into the EUR row at rate 1:
// the uppercase ISO CHECK that rejects them is a v0.3.7 contract migration, and
// this migration runs before the contract phase. Missing historical rates are
// filled from the ECB full history feed. A dry run reports counts and writes
// nothing.
func EURLedgerConversion(settings Settings) datamigrate.Migration {
	conversion := &eurLedgerConversion{fetch: settings.Fetch, now: time.Now}
	return datamigrate.Migration{
		ID:                  EURLedgerConversionID,
		Service:             "purser",
		IntroducedIn:        "v0.3.11",
		RequiredBeforePhase: "contract",
		Description:         "convert non-EUR prepaid balances into the EUR ledger at historical ECB rates and record FX fields on money rows",
		Irreversible:        true,
		Run:                 conversion.run,
		Verify:              VerifyEURLedgerConversion,
		Scopes:              conversion.scopes,
	}
}

type eurLedgerConversion struct {
	fetch fx.Fetcher
	now   func() time.Time

	historyMu sync.Mutex
	history   []fx.Rate
}

const eurLedgerConversionScopesSQL = `
SELECT DISTINCT tenant_id::text FROM (
    SELECT tenant_id FROM purser.prepaid_balances WHERE currency <> 'EUR'
    UNION SELECT tenant_id FROM purser.usage_reservations WHERE currency <> 'EUR'
    UNION SELECT tenant_id FROM purser.prepaid_usage_settlements WHERE currency <> 'EUR'
    UNION SELECT tenant_id FROM purser.pending_topups WHERE fx_source IS NULL
    UNION SELECT tenant_id FROM purser.crypto_wallets WHERE purpose = 'prepaid' AND fx_source IS NULL
    UNION SELECT tenant_id FROM purser.x402_payment_quotes WHERE fx_source IS NULL
    UNION SELECT tenant_id FROM purser.payment_reversals WHERE fx_source IS NULL
    UNION SELECT invoice.tenant_id
          FROM purser.billing_payments AS payment
          JOIN purser.billing_invoices AS invoice ON invoice.id = payment.invoice_id
          WHERE payment.fx_source IS NULL
) AS pending
ORDER BY 1`

// scopes returns one tenant scope per tenant with work, or the whole-job scope
// when no tenant has any, so a converged database completes immediately.
func (m *eurLedgerConversion) scopes(ctx context.Context, db datamigrate.DB) ([]datamigrate.ScopeKey, error) {
	rows, err := db.QueryContext(ctx, eurLedgerConversionScopesSQL)
	if err != nil {
		return nil, fmt.Errorf("list tenants with non-EUR ledger rows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var scopes []datamigrate.ScopeKey
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			return nil, err
		}
		scopes = append(scopes, datamigrate.ScopeKey{Kind: "tenant", Value: tenantID})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(scopes) == 0 {
		return []datamigrate.ScopeKey{{}}, nil
	}
	return scopes, nil
}

type balanceRow struct {
	id             string
	currency       string
	balanceCents   int64
	remainderMicro int64
}

type movement struct {
	transactionID string
	amountCents   int64
	referenceType string
	rateDate      time.Time
}

type conversion struct {
	balance   balanceRow
	canonical string
	movements []movement
}

type sourceRow struct {
	table       string
	id          string
	amountCents int64
	currency    string
	rateDate    time.Time
}

type tenantPlan struct {
	tenantID    string
	folds       []balanceRow
	conversions []conversion
	sources     []sourceRow
	residuals   []string
	scanned     int64
}

func (p *tenantPlan) rateNeeds() []rateNeed {
	var needs []rateNeed
	for _, c := range p.conversions {
		for _, mv := range c.movements {
			needs = append(needs, rateNeed{currency: c.canonical, date: fx.Date(mv.rateDate)})
		}
	}
	for _, source := range p.sources {
		needs = append(needs, rateNeed{currency: source.currency, date: fx.Date(source.rateDate)})
	}
	return needs
}

type rateNeed struct {
	currency string
	date     time.Time
}

func (m *eurLedgerConversion) run(ctx context.Context, db datamigrate.DB, opts datamigrate.RunOptions) (datamigrate.Progress, error) {
	if opts.Scope.IsZero() {
		return datamigrate.Progress{Done: true}, nil
	}
	if opts.Scope.Kind != "tenant" {
		return datamigrate.Progress{}, fmt.Errorf("%s runs per tenant scope, got %s", EURLedgerConversionID, opts.Scope)
	}
	tenantID := opts.Scope.Value
	if _, err := uuid.Parse(tenantID); err != nil {
		return datamigrate.Progress{}, fmt.Errorf("tenant scope %q: %w", tenantID, err)
	}

	plan, err := buildTenantPlan(ctx, db, tenantID, false)
	if err != nil {
		return datamigrate.Progress{}, err
	}
	if opts.DryRun {
		return datamigrate.Progress{
			Scanned: plan.scanned,
			Changed: plannedChanges(plan),
			Errors:  int64(len(plan.residuals)),
			Done:    true,
		}, nil
	}
	if len(plan.residuals) > 0 {
		return datamigrate.Progress{}, residualError(plan)
	}

	fxDB, ok := db.(purserdb.DBTX)
	if !ok {
		return datamigrate.Progress{}, errors.New("EUR ledger conversion needs a database handle that supports prepared statements")
	}
	if rateErr := m.ensureRates(ctx, fxDB, plan.rateNeeds()); rateErr != nil {
		return datamigrate.Progress{}, fmt.Errorf("tenant %s: %w", tenantID, rateErr)
	}

	beginner, ok := db.(interface {
		BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
	})
	if !ok {
		return datamigrate.Progress{}, errors.New("EUR ledger conversion needs a database handle that starts transactions")
	}
	tx, err := beginner.BeginTx(ctx, nil)
	if err != nil {
		return datamigrate.Progress{}, err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort after commit
	changed, err := applyTenantConversion(ctx, tx, tenantID)
	if err != nil {
		return datamigrate.Progress{}, err
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return datamigrate.Progress{}, fmt.Errorf("commit tenant %s conversion: %w", tenantID, commitErr)
	}
	return datamigrate.Progress{Scanned: plan.scanned, Changed: changed, Done: true}, nil
}

func plannedChanges(plan *tenantPlan) int64 {
	changes := int64(len(plan.folds) + len(plan.sources))
	for _, c := range plan.conversions {
		changes += int64(len(c.movements)) + 1
	}
	return changes
}

func residualError(plan *tenantPlan) error {
	return fmt.Errorf("tenant %s cannot convert to the EUR ledger: %s", plan.tenantID, strings.Join(plan.residuals, "; "))
}

// buildTenantPlan reads everything the tenant's conversion changes. With lock
// set it locks the tenant's balance rows first so the plan cannot move under a
// concurrent credit.
func buildTenantPlan(ctx context.Context, db datamigrate.DB, tenantID string, lock bool) (*tenantPlan, error) {
	plan := &tenantPlan{tenantID: tenantID}
	if lock {
		if _, lockErr := db.ExecContext(ctx, `SELECT 1 FROM purser.prepaid_balances WHERE tenant_id = $1::uuid FOR UPDATE`, tenantID); lockErr != nil {
			return nil, fmt.Errorf("lock tenant %s balances: %w", tenantID, lockErr)
		}
	}
	balances, err := readForeignBalances(ctx, db, tenantID)
	if err != nil {
		return nil, err
	}

	for _, balance := range balances {
		plan.scanned++
		canonical := strings.ToUpper(strings.TrimSpace(balance.currency))
		switch canonical {
		case fx.EUR:
			plan.folds = append(plan.folds, balance)
			continue
		case fx.USD, fx.GBP:
		default:
			plan.residuals = append(plan.residuals, fmt.Sprintf("balance row in unsupported currency %q holds %d cents", balance.currency, balance.balanceCents))
			continue
		}
		movements, movementErr := readMovements(ctx, db, tenantID, canonical)
		if movementErr != nil {
			return nil, movementErr
		}
		plan.scanned += int64(len(movements))
		var traced int64
		for _, mv := range movements {
			traced += mv.amountCents
		}
		if residual := balance.balanceCents - traced; residual != 0 || balance.remainderMicro != 0 {
			plan.residuals = append(plan.residuals, fmt.Sprintf(
				"%s balance of %d cents has a residual of %d cents and %d micro-units not traced to top-ups, crypto wallets, or reversals",
				balance.currency, balance.balanceCents, residual, balance.remainderMicro))
			continue
		}
		plan.conversions = append(plan.conversions, conversion{balance: balance, canonical: canonical, movements: movements})
	}

	var foreignReservations, foreignSettlements, orphanQuotes int64
	if err = db.QueryRowContext(ctx, `
SELECT
    (SELECT COUNT(*) FROM purser.usage_reservations WHERE tenant_id = $1::uuid AND UPPER(currency) <> 'EUR'),
    (SELECT COUNT(*) FROM purser.prepaid_usage_settlements WHERE tenant_id = $1::uuid AND UPPER(currency) <> 'EUR'),
    (SELECT COUNT(*) FROM purser.x402_payment_quotes WHERE tenant_id = $1::uuid AND fx_source IS NULL AND credit_currency <> 'EUR')`,
		tenantID).Scan(&foreignReservations, &foreignSettlements, &orphanQuotes); err != nil {
		return nil, fmt.Errorf("count tenant %s non-EUR usage rows: %w", tenantID, err)
	}
	if foreignReservations > 0 {
		plan.residuals = append(plan.residuals, fmt.Sprintf("%d usage reservations are not in EUR", foreignReservations))
	}
	if foreignSettlements > 0 {
		plan.residuals = append(plan.residuals, fmt.Sprintf("%d prepaid usage settlements are not in EUR", foreignSettlements))
	}
	if orphanQuotes > 0 {
		plan.residuals = append(plan.residuals, fmt.Sprintf("%d x402 quotes credit a currency other than EUR", orphanQuotes))
	}

	sources, err := readForeignSources(ctx, db, tenantID)
	if err != nil {
		return nil, err
	}
	for _, source := range sources {
		plan.scanned++
		if source.currency != fx.USD && source.currency != fx.GBP {
			plan.residuals = append(plan.residuals, fmt.Sprintf("%s row %s is in unsupported currency %q", source.table, source.id, source.currency))
			continue
		}
		plan.sources = append(plan.sources, source)
	}
	return plan, nil
}

func readForeignBalances(ctx context.Context, db datamigrate.DB, tenantID string) ([]balanceRow, error) {
	rows, err := db.QueryContext(ctx, `
SELECT id::text, currency, balance_cents, balance_remainder_micro
FROM purser.prepaid_balances
WHERE tenant_id = $1::uuid AND currency <> 'EUR'
ORDER BY currency`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("read tenant %s non-EUR balances: %w", tenantID, err)
	}
	defer func() { _ = rows.Close() }()
	var balances []balanceRow
	for rows.Next() {
		var row balanceRow
		if err := rows.Scan(&row.id, &row.currency, &row.balanceCents, &row.remainderMicro); err != nil {
			return nil, err
		}
		balances = append(balances, row)
	}
	return balances, rows.Err()
}

// readMovements returns the balance transactions that moved a tenant's
// balance in currency: credits of its top-ups and crypto wallets and debits of
// its payment reversals. A reversal of a top-up converts at the rate of the
// top-up's credit so a full refund nets to zero in EUR.
func readMovements(ctx context.Context, db datamigrate.DB, tenantID, currency string) ([]movement, error) {
	rows, err := db.QueryContext(ctx, `
SELECT bt.id::text, bt.amount_cents, bt.reference_type,
       COALESCE(topup_credit.created_at, bt.created_at, NOW())
FROM purser.balance_transactions AS bt
LEFT JOIN purser.pending_topups AS topup
    ON bt.reference_type = 'topup' AND topup.id = bt.reference_id AND topup.tenant_id = bt.tenant_id
LEFT JOIN purser.crypto_wallets AS wallet
    ON bt.reference_type IN ('crypto_payment', 'crypto_invoice_overpayment')
   AND wallet.id = bt.reference_id AND wallet.tenant_id = bt.tenant_id
LEFT JOIN purser.payment_reversals AS reversal
    ON bt.reference_type = 'payment_reversal' AND reversal.id = bt.reference_id AND reversal.tenant_id = bt.tenant_id
LEFT JOIN purser.balance_transactions AS topup_credit
    ON reversal.pending_topup_id IS NOT NULL
   AND topup_credit.tenant_id = bt.tenant_id
   AND topup_credit.reference_type = 'topup'
   AND topup_credit.reference_id = reversal.pending_topup_id
WHERE bt.tenant_id = $1::uuid
  AND COALESCE(UPPER(topup.currency), UPPER(wallet.credited_amount_currency), UPPER(reversal.currency)) = $2
ORDER BY bt.created_at, bt.id`, tenantID, currency)
	if err != nil {
		return nil, fmt.Errorf("read tenant %s %s balance movements: %w", tenantID, currency, err)
	}
	defer func() { _ = rows.Close() }()
	var movements []movement
	for rows.Next() {
		var mv movement
		if err := rows.Scan(&mv.transactionID, &mv.amountCents, &mv.referenceType, &mv.rateDate); err != nil {
			return nil, err
		}
		movements = append(movements, mv)
	}
	return movements, rows.Err()
}

// readForeignSources returns the tenant's non-EUR money rows without FX
// fields, each with the date whose rate converts it: the credit date for
// credited top-ups and wallets, the reversed top-up's credit date for
// reversals, and the confirmation date for invoice payments. A prepaid crypto
// wallet without an EUR credit or EUR quote is USD-denominated.
func readForeignSources(ctx context.Context, db datamigrate.DB, tenantID string) ([]sourceRow, error) {
	rows, err := db.QueryContext(ctx, `
SELECT 'pending_topups', topup.id::text, topup.amount_cents, UPPER(topup.currency),
       COALESCE((SELECT MIN(credit.created_at) FROM purser.balance_transactions AS credit
                 WHERE credit.tenant_id = topup.tenant_id AND credit.reference_type = 'topup'
                   AND credit.reference_id = topup.id), topup.created_at, NOW())
FROM purser.pending_topups AS topup
WHERE topup.tenant_id = $1::uuid AND topup.fx_source IS NULL AND UPPER(topup.currency) <> 'EUR'
UNION ALL
SELECT 'crypto_wallets', wallet.id::text, COALESCE(wallet.credited_amount_cents, wallet.expected_amount_cents),
       COALESCE(UPPER(wallet.credited_amount_currency), 'USD'),
       COALESCE((SELECT MIN(credit.created_at) FROM purser.balance_transactions AS credit
                 WHERE credit.tenant_id = wallet.tenant_id AND credit.reference_type = 'crypto_payment'
                   AND credit.reference_id = wallet.id), wallet.completed_at, wallet.created_at, NOW())
FROM purser.crypto_wallets AS wallet
WHERE wallet.tenant_id = $1::uuid AND wallet.purpose = 'prepaid' AND wallet.fx_source IS NULL
  AND NOT (wallet.credited_amount_currency = 'EUR'
           OR (wallet.credited_amount_currency IS NULL AND wallet.quoted_usd_to_eur_rate IS NOT NULL))
UNION ALL
SELECT 'payment_reversals', reversal.id::text, reversal.amount_cents, UPPER(reversal.currency),
       COALESCE((SELECT MIN(credit.created_at) FROM purser.balance_transactions AS credit
                 WHERE credit.tenant_id = reversal.tenant_id AND credit.reference_type = 'topup'
                   AND credit.reference_id = reversal.pending_topup_id), reversal.created_at)
FROM purser.payment_reversals AS reversal
WHERE reversal.tenant_id = $1::uuid AND reversal.fx_source IS NULL AND UPPER(reversal.currency) <> 'EUR'
UNION ALL
SELECT 'billing_payments', payment.id::text, ROUND(payment.amount * 100)::bigint, UPPER(payment.currency),
       COALESCE(payment.confirmed_at, payment.created_at, NOW())
FROM purser.billing_payments AS payment
JOIN purser.billing_invoices AS invoice ON invoice.id = payment.invoice_id
WHERE invoice.tenant_id = $1::uuid AND payment.fx_source IS NULL AND UPPER(payment.currency) <> 'EUR'
ORDER BY 1, 2`, tenantID)
	if err != nil {
		return nil, fmt.Errorf("read tenant %s non-EUR money rows: %w", tenantID, err)
	}
	defer func() { _ = rows.Close() }()
	var sources []sourceRow
	for rows.Next() {
		var source sourceRow
		if err := rows.Scan(&source.table, &source.id, &source.amountCents, &source.currency, &source.rateDate); err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return sources, rows.Err()
}

// ensureRates stores the ECB history rates that the needs lack. It fetches the
// full history feed at most once per process and stores only the rates within
// MaxReferenceAgeDays before each needed date.
func (m *eurLedgerConversion) ensureRates(ctx context.Context, db purserdb.DBTX, needs []rateNeed) error {
	var missing []rateNeed
	for _, need := range needs {
		_, err := fx.Lookup(ctx, db, need.currency, need.date)
		switch {
		case err == nil:
		case errors.Is(err, fx.ErrNoRate), errors.Is(err, fx.ErrStaleRate):
			missing = append(missing, need)
		default:
			return err
		}
	}
	if len(missing) == 0 {
		return nil
	}
	history, err := m.historyRates(ctx)
	if err != nil {
		return fmt.Errorf("fill historical ECB rates: %w", err)
	}
	var fill []fx.Rate
	stored := make(map[string]bool)
	for _, rate := range history {
		for _, need := range missing {
			age := fx.AgeDays(rate.ReferenceDate, need.date)
			if rate.Currency != need.currency || age < 0 || age > fx.MaxReferenceAgeDays {
				continue
			}
			key := rate.Currency + rate.ReferenceDate.Format(time.DateOnly)
			if !stored[key] {
				stored[key] = true
				fill = append(fill, rate)
			}
			break
		}
	}
	if err := fx.Upsert(ctx, db, fill, m.now()); err != nil {
		return err
	}
	for _, need := range missing {
		if _, err := fx.Lookup(ctx, db, need.currency, need.date); err != nil {
			return fmt.Errorf("no ECB rate for %s on %s after filling history: %w", need.currency, need.date.Format(time.DateOnly), err)
		}
	}
	return nil
}

func (m *eurLedgerConversion) historyRates(ctx context.Context) ([]fx.Rate, error) {
	m.historyMu.Lock()
	defer m.historyMu.Unlock()
	if m.history != nil {
		return m.history, nil
	}
	rates, err := fx.FetchRates(ctx, m.fetch, fx.FeedHistory)
	if err != nil {
		return nil, err
	}
	m.history = rates
	return rates, nil
}

// applyTenantConversion re-plans under the tenant's balance lock and applies
// it. Every rate it needs was stored by ensureRates; a rate that is still
// missing fails the scope without writing.
func applyTenantConversion(ctx context.Context, tx *sql.Tx, tenantID string) (int64, error) {
	plan, planErr := buildTenantPlan(ctx, tx, tenantID, true)
	if planErr != nil {
		return 0, planErr
	}
	if len(plan.residuals) > 0 {
		return 0, residualError(plan)
	}
	var changed int64

	if len(plan.folds) > 0 || len(plan.conversions) > 0 {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency, low_balance_threshold_cents, created_at, updated_at)
VALUES ($1::uuid, 0, 'EUR', 500, NOW(), NOW())
ON CONFLICT (tenant_id, currency) DO NOTHING`, tenantID); err != nil {
			return 0, fmt.Errorf("ensure tenant %s EUR balance: %w", tenantID, err)
		}
	}
	var eurBalance, eurRemainder int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(balance_cents), 0), COALESCE(MAX(balance_remainder_micro), 0)
FROM purser.prepaid_balances
WHERE tenant_id = $1::uuid AND currency = 'EUR'`, tenantID).Scan(&eurBalance, &eurRemainder); err != nil {
		return 0, fmt.Errorf("read tenant %s EUR balance: %w", tenantID, err)
	}
	startBalance, startRemainder := eurBalance, eurRemainder

	insertTransaction := func(amount int64, referenceType, referenceID, description string) (bool, error) {
		result, err := tx.ExecContext(ctx, `
INSERT INTO purser.balance_transactions (
    tenant_id, amount_cents, balance_after_cents, transaction_type, description,
    reference_id, reference_type, actor_kind, reason
) VALUES ($1::uuid, $2, $3, $4, $5, $6::uuid, $7, 'system', $8)
ON CONFLICT (tenant_id, reference_type, reference_id)
WHERE reference_type IS NOT NULL AND reference_id IS NOT NULL
DO NOTHING`,
			tenantID, amount, eurBalance+amount, fxConversionTransaction, description,
			referenceID, referenceType, "EUR ledger conversion "+EURLedgerConversionID)
		if err != nil {
			return false, fmt.Errorf("record %s transaction for %s: %w", referenceType, referenceID, err)
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return false, err
		}
		if inserted == 1 {
			eurBalance += amount
			changed++
		}
		return inserted == 1, nil
	}

	for _, fold := range plan.folds {
		inserted, err := insertTransaction(fold.balanceCents, fxFoldReferenceType, fold.id,
			fmt.Sprintf("Folded %q balance row into EUR at rate 1", fold.currency))
		if err != nil {
			return 0, err
		}
		if inserted {
			eurRemainder += fold.remainderMicro
		}
		if err := deleteBalanceRow(ctx, tx, tenantID, fold.id); err != nil {
			return 0, err
		}
		changed++
	}

	for _, c := range plan.conversions {
		for _, mv := range c.movements {
			rate, err := fx.Lookup(ctx, tx, c.canonical, mv.rateDate)
			if err != nil {
				return 0, fmt.Errorf("tenant %s %s transaction %s: %w", tenantID, c.canonical, mv.transactionID, err)
			}
			eurAmount, err := fx.ToEUR(mv.amountCents, rate)
			if err != nil {
				return 0, err
			}
			description := fmt.Sprintf("Converted %d %s cents (%s) to EUR at the ECB %s rate of %s %s per EUR",
				mv.amountCents, c.canonical, mv.referenceType, rate.ReferenceDate.Format(time.DateOnly), rate.UnitsPerEUR.String(), c.canonical)
			if _, err := insertTransaction(eurAmount, fxConversionReferenceType, mv.transactionID, description); err != nil {
				return 0, err
			}
		}
		if err := deleteBalanceRow(ctx, tx, tenantID, c.balance.id); err != nil {
			return 0, err
		}
		changed++
	}

	carry := eurRemainder / microPerCent
	eurRemainder %= microPerCent
	if eurRemainder < 0 {
		carry--
		eurRemainder += microPerCent
	}
	eurBalance += carry
	if eurBalance != startBalance || eurRemainder != startRemainder {
		if _, err := tx.ExecContext(ctx, `
UPDATE purser.prepaid_balances
SET balance_cents = $2, balance_remainder_micro = $3, updated_at = NOW()
WHERE tenant_id = $1::uuid AND currency = 'EUR'`, tenantID, eurBalance, eurRemainder); err != nil {
			return 0, fmt.Errorf("update tenant %s EUR balance: %w", tenantID, err)
		}
	}

	for _, source := range plan.sources {
		rate, err := fx.Lookup(ctx, tx, source.currency, source.rateDate)
		if err != nil {
			return 0, fmt.Errorf("tenant %s %s row %s: %w", tenantID, source.table, source.id, err)
		}
		eurAmount, err := fx.ToEUR(source.amountCents, rate)
		if err != nil {
			return 0, err
		}
		updated, err := setSourceFX(ctx, tx, source, eurAmount, rate)
		if err != nil {
			return 0, err
		}
		changed += updated
	}

	identity, err := setTenantIdentityFX(ctx, tx, tenantID)
	if err != nil {
		return 0, err
	}
	changed += identity

	for _, statement := range []string{
		`UPDATE purser.usage_reservations SET currency = 'EUR' WHERE tenant_id = $1::uuid AND UPPER(currency) = 'EUR' AND currency <> 'EUR'`,
		`UPDATE purser.prepaid_usage_settlements SET currency = 'EUR' WHERE tenant_id = $1::uuid AND UPPER(currency) = 'EUR' AND currency <> 'EUR'`,
	} {
		result, err := tx.ExecContext(ctx, statement, tenantID)
		if err != nil {
			return 0, fmt.Errorf("fold tenant %s usage currency spelling: %w", tenantID, err)
		}
		folded, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		changed += folded
	}
	return changed, nil
}

func deleteBalanceRow(ctx context.Context, tx *sql.Tx, tenantID, id string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM purser.prepaid_balances WHERE tenant_id = $1::uuid AND id = $2::uuid`, tenantID, id); err != nil {
		return fmt.Errorf("delete tenant %s balance row %s: %w", tenantID, id, err)
	}
	return nil
}

func setSourceFX(ctx context.Context, tx *sql.Tx, source sourceRow, eurAmount int64, rate fx.Rate) (int64, error) {
	var table string
	switch source.table {
	case "pending_topups", "crypto_wallets", "payment_reversals", "billing_payments":
		table = "purser." + source.table
	default:
		return 0, fmt.Errorf("unexpected FX source table %q", source.table)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE `+table+`
SET original_amount_cents = $2,
    original_currency = $3,
    eur_amount_cents = $4,
    fx_units_per_eur = $5::numeric,
    fx_source = 'ecb',
    fx_reference_date = $6::date
WHERE id = $1::uuid AND fx_source IS NULL`,
		source.id, source.amountCents, source.currency, eurAmount, rate.UnitsPerEUR.String(), rate.ReferenceDate.Format(time.DateOnly))
	if err != nil {
		return 0, fmt.Errorf("record FX fields on %s %s: %w", source.table, source.id, err)
	}
	return result.RowsAffected()
}

// setTenantIdentityFX records identity FX on the tenant's EUR money rows and
// the quoted rate on its x402 quotes, matching the v0.3.11 postdeploy backfill
// for rows written after it ran.
func setTenantIdentityFX(ctx context.Context, tx *sql.Tx, tenantID string) (int64, error) {
	statements := []string{
		`UPDATE purser.pending_topups
SET original_amount_cents = amount_cents, original_currency = 'EUR', eur_amount_cents = amount_cents,
    fx_units_per_eur = 1, fx_source = 'identity',
    fx_reference_date = (COALESCE(created_at, NOW()) AT TIME ZONE 'UTC')::date
WHERE tenant_id = $1::uuid AND fx_source IS NULL AND UPPER(currency) = 'EUR'`,
		`UPDATE purser.crypto_wallets
SET original_amount_cents = COALESCE(credited_amount_cents, expected_amount_cents), original_currency = 'EUR',
    eur_amount_cents = COALESCE(credited_amount_cents, expected_amount_cents),
    fx_units_per_eur = 1, fx_source = 'identity',
    fx_reference_date = (COALESCE(quoted_at, created_at, NOW()) AT TIME ZONE 'UTC')::date
WHERE tenant_id = $1::uuid AND fx_source IS NULL AND purpose = 'prepaid' AND expected_amount_cents IS NOT NULL
  AND (credited_amount_currency = 'EUR' OR (credited_amount_currency IS NULL AND quoted_usd_to_eur_rate IS NOT NULL))`,
		`UPDATE purser.x402_payment_quotes
SET original_amount_cents = ROUND(amount_atomic / 10000)::bigint, original_currency = 'USD',
    eur_amount_cents = credit_amount_cents, fx_units_per_eur = ROUND(1 / eur_per_usd_rate, 10),
    fx_source = 'legacy_quote', fx_reference_date = (created_at AT TIME ZONE 'UTC')::date
WHERE tenant_id = $1::uuid AND fx_source IS NULL AND credit_currency = 'EUR'`,
		`UPDATE purser.payment_reversals
SET original_amount_cents = amount_cents, original_currency = 'EUR', eur_amount_cents = amount_cents,
    fx_units_per_eur = 1, fx_source = 'identity', fx_reference_date = (created_at AT TIME ZONE 'UTC')::date
WHERE tenant_id = $1::uuid AND fx_source IS NULL AND UPPER(currency) = 'EUR'`,
		`UPDATE purser.billing_payments AS payment
SET original_amount_cents = ROUND(payment.amount * 100)::bigint, original_currency = 'EUR',
    eur_amount_cents = ROUND(payment.amount * 100)::bigint, fx_units_per_eur = 1, fx_source = 'identity',
    fx_reference_date = (COALESCE(payment.confirmed_at, payment.created_at, NOW()) AT TIME ZONE 'UTC')::date
FROM purser.billing_invoices AS invoice
WHERE invoice.id = payment.invoice_id AND invoice.tenant_id = $1::uuid
  AND payment.fx_source IS NULL AND UPPER(payment.currency) = 'EUR'`,
	}
	var changed int64
	for _, statement := range statements {
		result, err := tx.ExecContext(ctx, statement, tenantID)
		if err != nil {
			return 0, fmt.Errorf("record identity FX for tenant %s: %w", tenantID, err)
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		changed += rows
	}
	return changed, nil
}

// EURLedgerRemainingSQL counts what the conversion has left: non-EUR balance,
// reservation, and settlement rows, and money rows without FX fields.
const EURLedgerRemainingSQL = `
SELECT
    (SELECT COUNT(*) FROM purser.prepaid_balances WHERE currency <> 'EUR'),
    (SELECT COUNT(*) FROM purser.usage_reservations WHERE currency <> 'EUR'),
    (SELECT COUNT(*) FROM purser.prepaid_usage_settlements WHERE currency <> 'EUR'),
    (SELECT COUNT(*) FROM purser.pending_topups WHERE fx_source IS NULL)
      + (SELECT COUNT(*) FROM purser.crypto_wallets WHERE purpose = 'prepaid' AND fx_source IS NULL)
      + (SELECT COUNT(*) FROM purser.x402_payment_quotes WHERE fx_source IS NULL)
      + (SELECT COUNT(*) FROM purser.payment_reversals WHERE fx_source IS NULL)
      + (SELECT COUNT(*) FROM purser.billing_payments WHERE fx_source IS NULL)`

// VerifyEURLedgerConversion fails while any non-EUR balance, reservation, or
// settlement row, or any money row without FX fields, remains.
func VerifyEURLedgerConversion(ctx context.Context, db datamigrate.DB) error {
	var balances, reservations, settlements, missingFX int64
	if err := db.QueryRowContext(ctx, EURLedgerRemainingSQL).Scan(&balances, &reservations, &settlements, &missingFX); err != nil {
		return fmt.Errorf("verify EUR ledger conversion: %w", err)
	}
	if balances+reservations+settlements+missingFX != 0 {
		return fmt.Errorf("EUR ledger conversion incomplete: %d non-EUR balance rows, %d non-EUR usage reservations, %d non-EUR prepaid usage settlements, %d money rows without FX fields",
			balances, reservations, settlements, missingFX)
	}
	return nil
}
