//go:build schema_verify

package foghorndb

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

func TestPlacementMembershipSnapshotStore_RealPG(t *testing.T) {
	verifyPlacementMembershipSnapshotStore(t, startFoghornCatalogPostgres(t))
}

func TestPlacementMembershipSnapshotStore_RealYugabyte(t *testing.T) {
	verifyPlacementMembershipSnapshotStore(t, startFoghornCatalogYugabyte(t))
}

// One row per cell changes only for a different revision observed no earlier
// than the stored one: an unchanged confirmation writes nothing and a replica
// that received an older revision late cannot replace a newer one.
func verifyPlacementMembershipSnapshotStore(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	q := New(db)
	if _, err := q.LoadPlacementMembershipSnapshot(ctx, "membership-cell"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("empty store load = %v", err)
	}
	base := time.Now().UTC().Truncate(time.Microsecond)
	store := func(revision string, observed time.Time) int64 {
		t.Helper()
		written, err := q.StorePlacementMembershipSnapshot(ctx, StorePlacementMembershipSnapshotParams{
			CellID: "membership-cell", Revision: revision, Payload: []byte(revision), ObservedAt: observed, ConfirmedAt: base,
		})
		if err != nil {
			t.Fatal(err)
		}
		return written
	}
	load := func() LoadPlacementMembershipSnapshotRow {
		t.Helper()
		row, err := q.LoadPlacementMembershipSnapshot(ctx, "membership-cell")
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	if written := store("rev-1", base); written != 1 {
		t.Fatalf("first store wrote %d rows", written)
	}
	if written := store("rev-1", base.Add(time.Minute)); written != 0 {
		t.Fatal("an unchanged revision rewrote the snapshot")
	}
	if written := store("rev-2", base.Add(2*time.Second)); written != 1 {
		t.Fatal("a newer revision was not stored")
	}
	if written := store("rev-1", base.Add(time.Second)); written != 0 {
		t.Fatal("an older revision replaced a newer one")
	}
	if row := load(); row.Revision != "rev-2" || string(row.Payload) != "rev-2" || !row.ObservedAt.Equal(base.Add(2*time.Second)) || !row.ConfirmedAt.Equal(base) {
		t.Fatalf("stored snapshot = %+v", row)
	}
}
