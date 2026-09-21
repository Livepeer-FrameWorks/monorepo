package mediaauthority

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type AuthorityReconciler func(context.Context, *commodorepb.RequestMediaAuthorityReplayRequest) (*commodorepb.RequestMediaAuthorityReplayResponse, error)

type recoveryCoordinator struct {
	mu   sync.Mutex
	pass *recoveryPass
}

type recoveryPass struct {
	afterKind, afterID string
	startedAt          time.Time
	coreWatermark      *timestamppb.Timestamp
	generation         uint64
	checked, current   bool
}

// Reconcile checks acknowledged floors and confirms current copies in bounded
// pages. The cursor survives between calls so a large catalog cannot starve its
// last page. Only a proven regression asks the control plane to replay delivery.
func (s *Store) Reconcile(ctx context.Context, reconcile AuthorityReconciler) error {
	s.recovery.mu.Lock()
	defer s.recovery.mu.Unlock()
	q := foghorndb.New(s.db)
	call := func(request *commodorepb.RequestMediaAuthorityReplayRequest) (*commodorepb.RequestMediaAuthorityReplayResponse, error) {
		request.ControlCellId = s.cellID
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		response, err := reconcile(callCtx, request)
		if err != nil {
			code := status.Code(err)
			reachable := code != codes.Unavailable && code != codes.DeadlineExceeded && code != codes.Canceled
			return nil, errors.Join(err, s.SettleFence(ctx, FenceCheck{Reachable: reachable, Unsupported: code == codes.Unimplemented}))
		}
		if response.GetRecoveryProtocol() != sharedauthority.RecoveryProtocol {
			return nil, errors.Join(errors.New("unsupported media authority recovery protocol: upgrade Commodore before Foghorn"),
				s.SettleFence(ctx, FenceCheck{Reachable: true, Unsupported: true}))
		}
		if response.GetObservedAt() == nil || !response.GetObservedAt().IsValid() {
			return nil, errors.Join(errors.New("media authority recovery omitted its database clock watermark"),
				s.SettleFence(ctx, FenceCheck{Reachable: true}))
		}
		// Observe fences raised by other replicas; reaching core is not itself
		// evidence that the local database was restored.
		if err := s.SettleFence(ctx, FenceCheck{Reachable: true}); err != nil {
			return nil, err
		}
		return response, nil
	}
	if s.recovery.pass == nil {
		asOf, err := q.BeginMediaAuthorityConfirmation(ctx)
		if err != nil {
			return err
		}
		held, err := s.HeldSummary(ctx, asOf)
		if err != nil {
			return err
		}
		response, err := call(&commodorepb.RequestMediaAuthorityReplayRequest{HeldCount: held.Count, HeldDigest: held.Digest(), AsOf: timestamppb.New(asOf)})
		if err != nil {
			return err
		}
		if response.GetSummaryChecked() && response.GetHeldMatches() {
			s.fence.mu.Lock()
			generation := s.fence.recoveryGeneration
			s.fence.mu.Unlock()
			required, err := q.HasMediaAuthorityConfirmationRequired(ctx, s.now().UTC())
			if err != nil {
				return err
			}
			if err := s.SettleFence(ctx, FenceCheck{Reachable: true, Checked: true, Matches: true, AsOf: asOf, InventoryComplete: !required, InventoryGeneration: generation}); err != nil {
				return err
			}
			if !s.RecoveryPending() {
				return nil
			}
		} else if !response.GetInventoryRequired() {
			return errors.New("media authority recovery returned neither a checked match nor an inventory request")
		}
		s.fence.mu.Lock()
		s.fence.recoveryPending = true
		generation := s.fence.recoveryGeneration
		s.fence.mu.Unlock()
		s.recovery.pass = &recoveryPass{startedAt: asOf, coreWatermark: response.GetObservedAt(), generation: generation, checked: true, current: true}
	}
	// At most 2,000 identities per invocation; the periodic worker resumes here.
	for range 4 {
		pass := s.recovery.pass
		generation := s.fence.confirmationGeneration()
		asOf, err := q.BeginMediaAuthorityConfirmation(ctx)
		if err != nil {
			return err
		}
		rows, err := q.ListHeldMediaAuthorityPage(ctx, foghorndb.ListHeldMediaAuthorityPageParams{AsOf: asOf, AfterKind: pass.afterKind, AfterID: pass.afterID, PageSize: sharedauthority.RecoveryPageSize})
		if err != nil {
			return err
		}
		request := &commodorepb.RequestMediaAuthorityReplayRequest{InventoryPage: true, AsOf: timestamppb.New(asOf), AfterKind: pass.afterKind, AfterId: pass.afterID, FinalPage: len(rows) < sharedauthority.RecoveryPageSize}
		request.AcknowledgedBefore = pass.coreWatermark
		requested := make(map[string]int64, len(rows))
		for _, row := range rows {
			request.Held = append(request.Held, &commodorepb.HeldMediaAuthority{AuthorityKind: row.AuthorityKind, AuthorityId: row.AuthorityID, AuthorityVersion: row.AuthorityVersion})
			requested[fenceKey(row.AuthorityKind, row.AuthorityID)] = row.AuthorityVersion
		}
		response, err := call(request)
		if err != nil {
			return err
		}
		pass.coreWatermark = response.GetObservedAt()
		if response.GetSummaryChecked() && !response.GetHeldMatches() {
			if err := s.SettleFence(ctx, FenceCheck{Reachable: true, Checked: true, AsOf: asOf}); err != nil {
				return err
			}
			// Corrections were queued for the cell. Do not replay that entire set
			// again for every remaining regressed page in this inventory.
			s.recovery.pass = nil
			return errors.New("media authority regression detected; awaiting queued corrections")
		}
		seen := make(map[string]bool, len(response.GetConfirmed()))
		for _, proof := range response.GetConfirmed() {
			key := fenceKey(proof.GetAuthorityKind(), proof.GetAuthorityId())
			if seen[key] || requested[key] == 0 || requested[key] != proof.GetAuthorityVersion() {
				return fmt.Errorf("invalid media authority confirmation for %q", key)
			}
			seen[key] = true
		}
		for _, proof := range response.GetConfirmed() {
			updated, err := q.ConfirmRecoveredMediaAuthority(ctx, foghorndb.ConfirmRecoveredMediaAuthorityParams{AuthorityKind: proof.GetAuthorityKind(), AuthorityID: proof.GetAuthorityId(), AuthorityVersion: proof.GetAuthorityVersion(), ConfirmedAt: asOf})
			if err != nil {
				return err
			}
			if updated > 0 {
				s.fence.confirm(proof.GetAuthorityKind(), proof.GetAuthorityId(), generation)
			}
		}
		pass.checked = pass.checked && response.GetSummaryChecked() && response.GetHeldMatches()
		pass.current = pass.current && len(seen) == len(requested)
		if request.FinalPage {
			s.recovery.pass = nil
			required, err := q.HasMediaAuthorityConfirmationRequired(ctx, s.now().UTC())
			if err != nil {
				return err
			}
			// Once a restore was proven, acknowledged floors alone are not enough:
			// every surviving copy must also be confirmed current before unfencing.
			matched := pass.checked && (!s.DurablyFenced() || pass.current)
			return s.SettleFence(ctx, FenceCheck{Reachable: true, Checked: matched, Matches: matched, AsOf: pass.startedAt,
				InventoryComplete: matched && !required, InventoryGeneration: pass.generation})
		}
		last := rows[len(rows)-1]
		pass.afterKind, pass.afterID = last.AuthorityKind, last.AuthorityID
	}
	return nil
}

func (s *Store) RecoveryInProgress() bool {
	s.recovery.mu.Lock()
	defer s.recovery.mu.Unlock()
	return s.recovery.pass != nil
}
