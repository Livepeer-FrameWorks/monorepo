package main

import (
	"context"
	"database/sql"
	"slices"
	"sync"
	"testing"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// memoryTierStore holds one tier row and applies UpdateBillingTierStripeIDs
// as its SQL does: only while the row holds the IDs the caller read.
// recordedElsewhere are prices other tiers or cluster prices record.
type memoryTierStore struct {
	mu                sync.Mutex
	row               purserdb.ListActivePaidBillingTiersForStripeRow
	recordedElsewhere []string
}

func (s *memoryTierStore) ListActivePaidBillingTiersForStripe(context.Context) ([]purserdb.ListActivePaidBillingTiersForStripeRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return []purserdb.ListActivePaidBillingTiersForStripeRow{s.row}, nil
}

func (s *memoryTierStore) UpdateBillingTierStripeIDs(_ context.Context, arg purserdb.UpdateBillingTierStripeIDsParams) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if arg.ID != s.row.ID || arg.ReadStripeProductID != s.row.StripeProductID || arg.ReadStripePriceIDMonthly != s.row.StripePriceIDMonthly {
		return 0, nil
	}
	s.row.StripeProductID = arg.StripeProductID
	s.row.StripePriceIDMonthly = arg.StripePriceIDMonthly
	return 1, nil
}

func (s *memoryTierStore) IsStripePriceRecorded(_ context.Context, priceID string) (sql.NullBool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recorded := s.row.StripePriceIDMonthly.String == priceID || slices.Contains(s.recordedElsewhere, priceID)
	return sql.NullBool{Bool: recorded, Valid: true}, nil
}

// racingCatalog hands each replica the price it is set to sync to, created
// by that replica's call, and records deactivations.
type racingCatalog struct {
	mu          sync.Mutex
	prices      map[string]string
	deactivated []string
	syncing     map[string]chan struct{}
	release     map[string]chan struct{}
}

func (c *racingCatalog) SyncTier(ctx context.Context, knownProductID, _, _, _, _ string, _ decimal.Decimal, _ string) (string, string, bool, error) {
	replica := ctx.Value(replicaKey{}).(string)
	close(c.syncing[replica])
	<-c.release[replica]
	return knownProductID, c.prices[replica], true, nil
}

func (c *racingCatalog) DeactivatePrice(_ context.Context, priceID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deactivated = append(c.deactivated, priceID)
	return nil
}

type replicaKey struct{}

// raceTierSync runs replicas a and b over one tier recorded with price_old.
// Both read the tier before either records a new price; a records first.
func raceTierSync(t *testing.T, prices map[string]string, recordedElsewhere ...string) (*memoryTierStore, *racingCatalog) {
	t.Helper()
	store := &memoryTierStore{recordedElsewhere: recordedElsewhere, row: purserdb.ListActivePaidBillingTiersForStripeRow{
		ID: uuid.New(), TierName: "pro", DisplayName: "Pro", BasePrice: "39.95", Currency: "EUR",
		StripeProductID:      sql.NullString{String: "prod_pro", Valid: true},
		StripePriceIDMonthly: sql.NullString{String: "price_old", Valid: true},
	}}
	catalog := &racingCatalog{
		prices:  prices,
		syncing: map[string]chan struct{}{"a": make(chan struct{}), "b": make(chan struct{})},
		release: map[string]chan struct{}{"a": make(chan struct{}), "b": make(chan struct{})},
	}
	run := func(replica string, done chan<- error) {
		ctx := context.WithValue(context.Background(), replicaKey{}, replica)
		done <- syncBillingTiersWithStripeLocked(ctx, store, catalog, logging.NewLogger())
	}
	aDone, bDone := make(chan error, 1), make(chan error, 1)
	go run("a", aDone)
	go run("b", bDone)
	<-catalog.syncing["a"]
	<-catalog.syncing["b"]
	close(catalog.release["a"])
	if err := <-aDone; err != nil {
		t.Fatal(err)
	}
	close(catalog.release["b"])
	if err := <-bDone; err != nil {
		t.Fatal(err)
	}
	if got := store.row.StripePriceIDMonthly.String; got != prices["a"] {
		t.Fatalf("recorded price = %s, want the first recorder's %s", got, prices["a"])
	}
	return store, catalog
}

// The replica that records second neither overwrites the recorded IDs nor
// deactivates the price the first replica replaced; it retires only the
// duplicate price it created itself.
func TestConcurrentTierSyncLoserRetiresOnlyItsOwnPrice(t *testing.T) {
	_, catalog := raceTierSync(t, map[string]string{"a": "price_a", "b": "price_b"})
	if want := []string{"price_old", "price_b"}; !slices.Equal(catalog.deactivated, want) {
		t.Fatalf("deactivated prices = %v, want %v: the winner's replaced price, then the loser's own duplicate", catalog.deactivated, want)
	}
}

// Stripe replayed the winner's create for the loser, so the price the loser
// holds is the recorded one: it must stay active.
func TestConcurrentTierSyncLoserKeepsTheRecordedPrice(t *testing.T) {
	_, catalog := raceTierSync(t, map[string]string{"a": "price_a", "b": "price_a"})
	if want := []string{"price_old"}; !slices.Equal(catalog.deactivated, want) {
		t.Fatalf("deactivated prices = %v, want only %v", catalog.deactivated, want)
	}
}

// Another tier records the loser's price, so it is not the loser's to retire.
func TestConcurrentTierSyncLoserKeepsAPriceRecordedElsewhere(t *testing.T) {
	_, catalog := raceTierSync(t, map[string]string{"a": "price_a", "b": "price_b"}, "price_b")
	if want := []string{"price_old"}; !slices.Equal(catalog.deactivated, want) {
		t.Fatalf("deactivated prices = %v, want only %v", catalog.deactivated, want)
	}
}
