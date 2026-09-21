//go:build schema_verify

package handlers

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
)

func TestMollieCursorAdvancePreservesConcurrentRewind_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	ctx := context.Background()
	queries := purserdb.New(db)
	if err := queries.EnsureMollieBalanceCursor(ctx, "balance"); err != nil {
		t.Fatal(err)
	}
	cursor, err := queries.GetMollieBalanceCursor(ctx, "balance")
	if err != nil {
		t.Fatal(err)
	}
	rewind := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	if _, err := db.ExecContext(ctx, `UPDATE purser.mollie_balance_cursors SET last_transaction_created_at = $1 WHERE balance_id = 'balance'`, rewind); err != nil {
		t.Fatal(err)
	}
	if err := queries.UpsertMollieBalanceCursor(ctx, purserdb.UpsertMollieBalanceCursorParams{
		BalanceID: "balance", ExpectedID: cursor.LastTransactionID, ExpectedCreatedAt: cursor.LastTransactionCreatedAt,
		LastTransactionID: sql.NullString{String: "newest", Valid: true}, LastTransactionCreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	after, err := queries.GetMollieBalanceCursor(ctx, "balance")
	if err != nil || after.LastTransactionID != "" || !after.LastTransactionCreatedAt.Time.Equal(rewind) {
		t.Fatalf("rewind overwritten: %+v, %v", after, err)
	}
}
