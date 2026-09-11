package grpc

import (
	"context"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
)

const (
	mediaAuthorityDeadlineDeliveryTimeout = 5 * time.Second
	mediaAuthorityDeadlineDeliveryLease   = 20 * time.Second
)

// Each worker refills independently. The database exposes one unlocked head per
// cell, so a slow cell cannot occupy all workers or hold a healthy cell's batch.
func (s *CommodoreServer) processMediaAuthorityDeadlineDeliveryWorker(ctx context.Context) {
	if s.db == nil || s.foghornPool == nil || s.quartermasterClient == nil {
		return
	}
	for ctx.Err() == nil {
		claimCtx, cancel := context.WithTimeout(ctx, time.Second)
		rows, err := commodoredb.New(s.db).ClaimMediaAuthorityDeadlineDelivery(claimCtx, commodoredb.ClaimMediaAuthorityDeadlineDeliveryParams{LeaseMs: mediaAuthorityDeadlineDeliveryLease.Milliseconds(), BatchSize: 1})
		cancel()
		if err != nil {
			s.logger.WithError(err).Warn("Failed to claim short-lease media authority delivery")
			return
		}
		if len(rows) == 0 {
			return
		}
		s.processMediaAuthorityDeliveryRow(ctx, commodoredb.ClaimMediaAuthorityDeliveriesRow(rows[0]), mediaAuthorityDeadlineDeliveryTimeout)
	}
}
