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

// A single claimant feeds bounded delivery slots. The database exposes one
// unlocked head per cell, and each completed delivery opens a slot immediately,
// so a slow cell neither multiplies idle claims nor blocks healthy cells.
func (s *CommodoreServer) processMediaAuthorityDeadlineDeliveryBatch(ctx context.Context) {
	if s.db == nil || s.foghornPool == nil || s.quartermasterClient == nil {
		return
	}
	completed := make(chan struct{}, mediaAuthorityDeliveryWorkers)
	running := 0
	for {
		if ctx.Err() != nil {
			for running > 0 {
				<-completed
				running--
			}
			return
		}

		available := mediaAuthorityDeliveryWorkers - running
		if available == 0 {
			select {
			case <-completed:
				running--
			case <-ctx.Done():
			}
			continue
		}

		claimCtx, cancel := context.WithTimeout(ctx, time.Second)
		rows, err := commodoredb.New(s.db).ClaimMediaAuthorityDeadlineDelivery(claimCtx, commodoredb.ClaimMediaAuthorityDeadlineDeliveryParams{LeaseMs: mediaAuthorityDeadlineDeliveryLease.Milliseconds(), BatchSize: int32(available)})
		cancel()
		if err != nil {
			s.logger.WithError(err).Warn("Failed to claim short-lease media authority delivery")
			for running > 0 {
				<-completed
				running--
			}
			return
		}
		if len(rows) == 0 {
			if running == 0 {
				return
			}
			select {
			case <-completed:
				running--
			case <-ctx.Done():
			}
			continue
		}
		for _, claimed := range rows {
			row := commodoredb.ClaimMediaAuthorityDeliveriesRow(claimed)
			running++
			go func() {
				s.processMediaAuthorityDeliveryRow(ctx, row, mediaAuthorityDeadlineDeliveryTimeout)
				completed <- struct{}{}
			}()
		}
	}
}
