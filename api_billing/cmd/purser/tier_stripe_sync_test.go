package main

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"frameworks/api_billing/internal/database/purserdb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/google/uuid"
	"github.com/shopspring/decimal"
)

// memoryTierStore holds one tier row and applies UpdateBillingTierStripeIDs
// as its SQL does: only while the row holds the IDs the caller read.
type memoryTierStore struct {
	mu  sync.Mutex
	row purserdb.ListActivePaidBillingTiersForStripeRow
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

// racingCatalog hands each replica its own new price, as two creates that
// Stripe did not deduplicate would, and records deactivations.
type racingCatalog struct {
	mu          sync.Mutex
	prices      map[string]string
	deactivated []string
	syncing     map[string]chan struct{}
	release     map[string]chan struct{}
}

func (c *racingCatalog) SyncTier(ctx context.Context, knownProductID, _, _, _, _ string, _ decimal.Decimal, _ string) (string, string, error) {
	replica := ctx.Value(replicaKey{}).(string)
	close(c.syncing[replica])
	<-c.release[replica]
	return knownProductID, c.prices[replica], nil
}

func (c *racingCatalog) DeactivatePrice(_ context.Context, priceID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deactivated = append(c.deactivated, priceID)
	return nil
}

type replicaKey struct{}

// Two replicas read the tier's recorded price before either records a new
// one. The first to record wins; the other must neither overwrite the
// recorded IDs nor deactivate the price the winner already replaced.
func TestConcurrentTierSyncLoserDoesNotOverwriteOrDeactivate(t *testing.T) {
	store := &memoryTierStore{row: purserdb.ListActivePaidBillingTiersForStripeRow{
		ID: uuid.New(), TierName: "pro", DisplayName: "Pro", BasePrice: "39.95", Currency: "EUR",
		StripeProductID:      sql.NullString{String: "prod_pro", Valid: true},
		StripePriceIDMonthly: sql.NullString{String: "price_old", Valid: true},
	}}
	catalog := &racingCatalog{
		prices:  map[string]string{"a": "price_a", "b": "price_b"},
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

	if got := store.row.StripePriceIDMonthly.String; got != "price_a" {
		t.Fatalf("recorded price = %s, want the first recorder's price_a", got)
	}
	if len(catalog.deactivated) != 1 || catalog.deactivated[0] != "price_old" {
		t.Fatalf("deactivated prices = %v, want only price_old, by the replica that recorded the new price", catalog.deactivated)
	}
}
