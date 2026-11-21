package jobs

import (
	"context"
	"database/sql"
	"sort"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/state"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

const processingFairnessThreshold = time.Minute

// Reservations survive dispatch: only a durable edge admission or a terminal
// job transition releases protection. Selection and edge enforcement share the
// same versioned record, so a delayed frame cannot resurrect a cleared hold.
func (d *ProcessingDispatcher) refreshProcessingReservations(ctx context.Context, tick *processingTickInventory) error {
	queries := foghorndb.New(d.db)
	if err := queries.ClearFinishedProcessingReservations(ctx); err != nil {
		return err
	}
	reservations, err := queries.ListProcessingMemoryReservations(ctx)
	if err != nil {
		return err
	}
	byNode := make(map[string]foghorndb.ListProcessingMemoryReservationsRow, len(reservations))
	byJob := make(map[string]string, len(reservations))
	for _, reservation := range reservations {
		byNode[reservation.NodeID] = reservation
		if reservation.JobID.Valid {
			byJob[reservation.JobID.String] = reservation.NodeID
		}
	}
	now := time.Now().UTC()
	waiters, err := queries.ListProcessingMemoryWaiters(ctx, foghorndb.ListProcessingMemoryWaitersParams{WaitThreshold: now.Add(-processingFairnessThreshold), UploadDeadline: now.Add(-24 * time.Hour)})
	if err != nil {
		return err
	}
	sm := state.DefaultManager()
	alive := make(map[string]bool)
	for _, id := range sm.AliveNodeIDs(time.Minute) {
		alive[id] = true
	}
	for _, waiter := range waiters {
		if err = ctx.Err(); err != nil {
			return err
		}
		if !clusterAccessibleForTenant(ctx, waiter.OriginClusterID.String, waiter.JobTenantID) {
			continue
		}
		inventory := tick.cluster(ctx, waiter.OriginClusterID.String)
		if inventory.err != nil {
			continue
		}
		var candidates []string
		budgets := make(map[string]uint64)
		for _, id := range inventory.ids {
			if waiter.PreferredNodeID.Valid && waiter.PreferredNodeID.String != id {
				continue
			}
			budget, known := inventory.budget(id, sm)
			if !known || budget < uint64(waiter.RequiredMemoryBytes) {
				continue
			}
			if held := byNode[id]; held.JobID.Valid && held.JobID.String != waiter.JobID {
				continue
			}
			candidates = append(candidates, id)
			budgets[id] = budget
		}
		healthy := func(id string) bool {
			n := sm.GetNodeState(id)
			return n != nil && n.IsHealthy && alive[id] && n.CapProcessing && nodeAcceptsProcessing(n, waiter.PreferredNodeID.Valid) && nodeEligibleForJobTenant(ctx, n, waiter.JobTenantID)
		}
		sort.Slice(candidates, func(i, j int) bool {
			a, b := candidates[i], candidates[j]
			if healthy(a) != healthy(b) {
				return healthy(a)
			}
			if budgets[a] != budgets[b] {
				return budgets[a] < budgets[b]
			}
			return a < b
		})
		if held := byJob[waiter.JobID]; held != "" {
			// Keep a valid hold stable. If its node left the registered inventory or
			// shrank, clear it before selecting another fitting node.
			if _, ok := budgets[held]; ok && byNode[held].RequiredMemoryBytes == waiter.RequiredMemoryBytes {
				continue
			}
			if _, err = queries.CancelProcessingNodeReservation(ctx, foghorndb.CancelProcessingNodeReservationParams{JobID: sql.NullString{String: waiter.JobID, Valid: true}, TenantID: sql.NullString{String: waiter.JobTenantID, Valid: true}}); err != nil {
				return err
			}
			delete(byJob, waiter.JobID)
			delete(byNode, held)
		}
		if len(candidates) == 0 {
			continue
		}
		id := candidates[0]
		changed, reserveErr := queries.ReserveProcessingNode(ctx, foghorndb.ReserveProcessingNodeParams{NodeID: id, JobID: waiter.JobID, TenantID: waiter.JobTenantID, RequiredBytes: waiter.RequiredMemoryBytes, NodeBudget: int64(budgets[id]), WaitThreshold: now.Add(-processingFairnessThreshold)})
		if reserveErr != nil {
			return reserveErr
		}
		if changed > 0 {
			byNode[id] = foghorndb.ListProcessingMemoryReservationsRow{NodeID: id, JobID: sql.NullString{String: waiter.JobID, Valid: true}, TenantID: waiter.JobTenantID, RequiredMemoryBytes: waiter.RequiredMemoryBytes}
			byJob[waiter.JobID] = id
		}
	}
	// Reload revisions after concurrent schedulers and publish explicit clears.
	reservations, err = queries.ListProcessingMemoryReservations(ctx)
	if err != nil {
		return err
	}
	for _, reservation := range reservations {
		if err = ctx.Err(); err != nil {
			return err
		}
		if !alive[reservation.NodeID] {
			continue
		}
		if !reservation.JobID.Valid && reservation.RequiredMemoryBytes > 0 {
			continue
		}
		update := &ipcpb.ProcessingMemoryReservation{JobId: reservation.JobID.String, Bytes: uint64(reservation.RequiredMemoryBytes), Version: uint64(reservation.Revision)}
		if sendErr := control.SendProcessingMemoryReservation(ctx, reservation.NodeID, update); sendErr != nil {
			d.logger.WithError(sendErr).WithField("node_id", reservation.NodeID).Debug("Processing memory protection awaits node connection")
		}
	}
	return nil
}
