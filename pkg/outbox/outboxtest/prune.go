// Package outboxtest checks an outbox table's CompletedPruner against a real
// database engine.
package outboxtest

import (
	"context"
	"database/sql"
	"sort"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
)

// PruneTable describes the rows VerifyCompletedPrune seeds into one outbox
// table: Columns and Values (SQL literals) fill its required columns.
type PruneTable struct {
	Table   string
	Columns string
	Values  string
}

// VerifyCompletedPrune seeds delivered rows older than outbox.CompletedRetention, a delivered row inside it and a
// pending row older than it. One purge call with a limit below the old delivered set must delete exactly that many;
// outbox.PruneCompletedBatches must then delete the rest and leave only the recent delivered and the pending row.
func VerifyCompletedPrune(t *testing.T, db *sql.DB, table PruneTable, purge outbox.PurgeBatchFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	insert := `INSERT INTO ` + table.Table + ` (` + table.Columns + `, created_at, completed_at)
VALUES (` + table.Values + `, now() - ($1::text)::interval, now() - ($2::text)::interval)
RETURNING id::text`
	insertPending := `INSERT INTO ` + table.Table + ` (` + table.Columns + `, created_at)
VALUES (` + table.Values + `, now() - interval '30 days')
RETURNING id::text`

	var expired []string
	for range 5 {
		var id string
		if err := db.QueryRowContext(ctx, insert, "9 days", "8 days").Scan(&id); err != nil {
			t.Fatalf("seed expired delivered row into %s: %v", table.Table, err)
		}
		expired = append(expired, id)
	}
	var recent, pending string
	if err := db.QueryRowContext(ctx, insert, "2 days", "1 day").Scan(&recent); err != nil {
		t.Fatalf("seed recent delivered row into %s: %v", table.Table, err)
	}
	if err := db.QueryRowContext(ctx, insertPending).Scan(&pending); err != nil {
		t.Fatalf("seed pending row into %s: %v", table.Table, err)
	}

	retention := int64(outbox.CompletedRetention.Seconds())
	first, err := purge(ctx, retention, 2)
	if err != nil {
		t.Fatalf("purge one batch of %s: %v", table.Table, err)
	}
	if first != 2 {
		t.Fatalf("purge of %s with limit 2 deleted %d rows, want 2", table.Table, first)
	}
	rest, err := outbox.PruneCompletedBatches(ctx, purge)
	if err != nil {
		t.Fatalf("prune %s: %v", table.Table, err)
	}
	if first+rest != int64(len(expired)) {
		t.Fatalf("prune %s deleted %d rows, want %d", table.Table, first+rest, len(expired))
	}

	rows, err := db.QueryContext(ctx, `SELECT id::text FROM `+table.Table)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var left []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left = append(left, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{recent, pending}
	sort.Strings(left)
	sort.Strings(want)
	if len(left) != len(want) || left[0] != want[0] || left[1] != want[1] {
		t.Fatalf("%s after prune holds %v, want only the recent delivered and the pending row %v", table.Table, left, want)
	}
}
