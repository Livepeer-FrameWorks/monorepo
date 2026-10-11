package main

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/database/foghorndb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/prometheus/client_golang/prometheus"
)

// placementMembershipStore keeps the cell's membership snapshot in the cell
// database, one row per cell.
type placementMembershipStore struct {
	db foghorndb.DBTX
}

func (store placementMembershipStore) LoadPlacementMembership(ctx context.Context, cellID string) (*balancer.StoredPlacementMembership, error) {
	row, err := foghorndb.New(store.db).LoadPlacementMembershipSnapshot(ctx, cellID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &balancer.StoredPlacementMembership{Revision: row.Revision, Payload: row.Payload, ObservedAt: row.ObservedAt, ConfirmedAt: row.ConfirmedAt}, nil
}

func (store placementMembershipStore) StorePlacementMembership(ctx context.Context, cellID string, membership balancer.StoredPlacementMembership) error {
	_, err := foghorndb.New(store.db).StorePlacementMembershipSnapshot(ctx, foghorndb.StorePlacementMembershipSnapshotParams{
		CellID: cellID, Revision: membership.Revision, Payload: membership.Payload,
		ObservedAt: membership.ObservedAt, ConfirmedAt: membership.ConfirmedAt,
	})
	return err
}

// currentPlacementMembershipSource follows the shared Quartermaster holder, so
// a reconnect replaces the client the membership stream and reads use.
func currentPlacementMembershipSource() balancer.PlacementMembershipSource {
	if client := releaseReconcilerClient.Load(); client != nil {
		return client
	}
	return nil
}

// startPlacementMembership loads the cell's stored membership before placement
// serves, so a restart while Quartermaster is unreachable keeps placing, and
// then keeps it current from Quartermaster on every replica.
func startPlacementMembership(db *sql.DB, cellID string, metrics *monitoring.MetricsCollector, logger logging.Logger) *balancer.PlacementMembership {
	membership := balancer.NewPlacementMembership(balancer.PlacementMembershipConfig{
		CellID: cellID, Source: currentPlacementMembershipSource, Store: placementMembershipStore{db: db}, Logger: logger,
	})
	loadCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := membership.Load(loadCtx); err != nil {
		logger.WithError(err).Warn("Stored placement membership is unavailable; placement waits for Quartermaster")
	}
	cancel()
	if metrics != nil {
		metrics.RegisterCustomMetric("placement_membership_snapshot_age_seconds", prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "foghorn_placement_membership_snapshot_age_seconds",
			Help: "Seconds since Quartermaster last confirmed the placement membership this replica places from; zero while Quartermaster predates membership",
		}, membership.AgeSeconds))
	}
	go membership.Run(context.Background())
	return membership
}
