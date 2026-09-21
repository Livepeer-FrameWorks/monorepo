//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/lib/pq"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

const (
	prepaidCurrencyRepairMigration     = "migrations/purser/v0.3.7/postdeploy/001_merge_prepaid_balance_currency_case.sql"
	prepaidCurrencyConstraintMigration = "migrations/purser/v0.3.7/contract/001_prepaid_balance_currency_iso.sql"
)

func TestPrepaidBalanceCurrencyRepairMigration_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()

	// The schema this migration repairs has no currency constraint on prepaid
	// balances; lowercase and non-EUR rows exist there.
	if _, err := db.ExecContext(ctx, `
		ALTER TABLE purser.prepaid_balances
		    DROP CONSTRAINT chk_prepaid_balances_currency_iso,
		    DROP CONSTRAINT IF EXISTS chk_prepaid_balances_ledger_currency
	`); err != nil {
		t.Fatalf("restore pre-contract prepaid balance shape: %v", err)
	}

	bothRows := uuid.NewString()
	lowercaseOnly := uuid.NewString()
	untouched := uuid.NewString()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO purser.prepaid_balances
		    (tenant_id, balance_cents, balance_remainder_micro, currency, low_balance_threshold_cents, updated_at)
		VALUES ($1, 1000, 9000, 'EUR', 700, NOW() - INTERVAL '1 day'),
		       ($1, 1500, 2500, 'eur', 500, NOW() - INTERVAL '1 day'),
		       ($2, 1200, 300, 'eur', 900, NOW() - INTERVAL '1 day'),
		       ($3, 42, 17, 'USD', 500, NOW() - INTERVAL '1 day')
	`, bothRows, lowercaseOnly, untouched); err != nil {
		t.Fatalf("seed prepaid balances: %v", err)
	}

	applyMigrationFile(t, db, prepaidCurrencyRepairMigration)
	want := map[string]string{
		bothRows:      "EUR:2501:1500:700",
		lowercaseOnly: "EUR:1200:300:900",
		untouched:     "USD:42:17:500",
	}
	first := prepaidBalanceSnapshot(t, db)
	for tenantID, row := range want {
		if first[tenantID].state != row {
			t.Fatalf("tenant %s balances = %q, want %q", tenantID, first[tenantID].state, row)
		}
	}
	if first[untouched].updatedAt == first[bothRows].updatedAt {
		t.Fatalf("untouched balance row was rewritten")
	}

	applyMigrationFile(t, db, prepaidCurrencyRepairMigration)
	second := prepaidBalanceSnapshot(t, db)
	for tenantID, row := range first {
		if second[tenantID] != row {
			t.Fatalf("rerun changed tenant %s: %+v -> %+v", tenantID, row, second[tenantID])
		}
	}

	applyMigrationFile(t, db, prepaidCurrencyConstraintMigration)
	_, err := db.ExecContext(ctx, `INSERT INTO purser.prepaid_balances (tenant_id, currency) VALUES ($1, 'eur')`, uuid.NewString())
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) || pqErr.Code != "23514" || pqErr.Constraint != "chk_prepaid_balances_currency_iso" {
		t.Fatalf("lowercase insert after contract = %v, want chk_prepaid_balances_currency_iso violation", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO purser.prepaid_balances (tenant_id, currency) VALUES ($1, 'EUR')`, uuid.NewString()); err != nil {
		t.Fatalf("uppercase insert after contract: %v", err)
	}
}

type prepaidBalanceSnapshotRow struct {
	state     string
	updatedAt string
}

func applyMigrationFile(t *testing.T, db *sql.DB, path string) {
	t.Helper()
	body, err := dbsql.Content.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if _, err := db.ExecContext(context.Background(), string(body)); err != nil {
		t.Fatalf("apply %s: %v", path, err)
	}
}

// prepaidBalanceSnapshot renders each tenant's rows as
// currency:balance:remainder:threshold, joined with "|" in currency order.
func prepaidBalanceSnapshot(t *testing.T, db *sql.DB) map[string]prepaidBalanceSnapshotRow {
	t.Helper()
	rows, err := db.QueryContext(context.Background(), `
		SELECT tenant_id::text, currency, balance_cents, balance_remainder_micro,
		       COALESCE(low_balance_threshold_cents, -1), updated_at::text
		FROM purser.prepaid_balances
		ORDER BY tenant_id, currency
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	snapshot := map[string]prepaidBalanceSnapshotRow{}
	for rows.Next() {
		var tenantID, currency, updatedAt string
		var balance, remainder, threshold int64
		if err := rows.Scan(&tenantID, &currency, &balance, &remainder, &threshold, &updatedAt); err != nil {
			t.Fatal(err)
		}
		entry := snapshot[tenantID]
		parts := []string{}
		if entry.state != "" {
			parts = append(parts, entry.state)
		}
		parts = append(parts, fmt.Sprintf("%s:%d:%d:%d", currency, balance, remainder, threshold))
		entry.state = strings.Join(parts, "|")
		if entry.updatedAt != "" {
			updatedAt = entry.updatedAt + "|" + updatedAt
		}
		entry.updatedAt = updatedAt
		snapshot[tenantID] = entry
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
