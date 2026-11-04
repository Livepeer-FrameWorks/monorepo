package control

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"frameworks/api_balancing/internal/database/foghorndb"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

type PublisherLeaseReleaser interface {
	AbandonPublisherLease(context.Context, string, string, string) error
	SyncActiveIngestPlacement(context.Context, string, []*pb.ActiveIngestStream, []*pb.ActiveIngestStream) (*pb.SyncActiveIngestPlacementResponse, error)
}

// RecoverPublisherAdmissions owns settlement after a request has stopped. The
// prepared row survives a crash; the edge's durable abandonment marker also
// covers a confirmation that committed after its response was lost.
func RecoverPublisherAdmissions(ctx context.Context, client PublisherLeaseReleaser) error {
	if db == nil {
		return nil
	}
	q := foghorndb.New(db)
	candidates, err := q.ListPublisherAdmissionsToSettle(ctx, IngestAdmissionWindow.Milliseconds())
	if err != nil {
		return err
	}
	settleErr := recoverPublisherBatch(ctx, len(candidates), func(i int) error {
		candidate := candidates[i]
		if candidate.Generation != "" {
			var settleErr error
			if candidate.PublisherAdmissionVersion == 0 {
				_, settleErr = RetireIngestSession(ctx, candidate.Generation, candidate.TenantID, candidate.StreamInternalName, candidate.Reason, logging.NewLogger())
			} else {
				request := IngestSessionRequest{TenantID: candidate.TenantID, InternalName: candidate.StreamInternalName, NodeID: candidate.NodeID, TriggerUUID: candidate.StartTriggerUuid, StreamID: candidate.StreamID, PlaybackID: streamEventPlaybackID(ctx, candidate.StreamInternalName)}
				_, settleErr = NewPublisherAdmissionRepository(db).Settle(ctx, request, candidate.Generation, candidate.Reason)
			}
			if settleErr != nil {
				return settleErr
			}
		}
		if candidate.Reason == IngestEndedAdmissionAbandoned {
			_, markErr := q.MarkPublisherSessionsSettled(ctx, foghorndb.MarkPublisherSessionsSettledParams{NodeID: candidate.NodeID, TriggerUuid: candidate.StartTriggerUuid})
			return markErr
		}
		return nil
	})
	if client == nil {
		return settleErr
	}
	// A marker is durable before the edge acknowledges settlement, even if no
	// generation exists locally. Commodore serializes it against a late acquire.
	markers, markerErr := q.ListPublisherAbandonmentsToForward(ctx)
	if markerErr == nil {
		markerErr = recoverPublisherBatch(ctx, len(markers), func(i int) error {
			marker := markers[i]
			// Old edges acquired leases without execution metadata. Keep the marker
			// pending until their exact tenant/stream/cluster/token owner is released.
			legacy, lookupErr := q.ListLegacyPublisherClaimReleasesByExecution(ctx, foghorndb.ListLegacyPublisherClaimReleasesByExecutionParams{NodeID: marker.NodeID, TriggerUuid: marker.StartTriggerUuid})
			if lookupErr != nil {
				return lookupErr
			}
			for _, owner := range legacy {
				if !owner.Ended {
					return errors.New("legacy publisher is not retired")
				}
				if owner.IngestClusterID.String == "" {
					continue
				}
				response, releaseErr := client.SyncActiveIngestPlacement(ctx, owner.IngestClusterID.String, nil, []*pb.ActiveIngestStream{{TenantId: owner.TenantID, InternalName: owner.StreamInternalName, ClusterId: owner.IngestClusterID.String, ClaimToken: owner.StartTriggerUuid}})
				if releaseErr != nil {
					return releaseErr
				}
				if response == nil || len(response.GetReleaseRefused()) > 0 {
					return errors.New("legacy publisher lease release was not confirmed")
				}
			}
			if abandonErr := client.AbandonPublisherLease(ctx, localControlCellID, marker.NodeID, marker.StartTriggerUuid); abandonErr != nil {
				return abandonErr
			}
			_, forwardErr := q.MarkPublisherAbandonmentForwarded(ctx, foghorndb.MarkPublisherAbandonmentForwardedParams{NodeID: marker.NodeID, TriggerUuid: marker.StartTriggerUuid})
			return forwardErr
		})
	}
	return errors.Join(settleErr, markerErr, releasePublisherLeases(ctx, client, q))
}

func releasePublisherLeases(ctx context.Context, client PublisherLeaseReleaser, q *foghorndb.Queries) error {
	releases, err := q.ListPublisherLeaseReleases(ctx)
	if err != nil || len(releases) == 0 {
		return err
	}
	batch := make([]*pb.ActiveIngestStream, 0, len(releases))
	for _, release := range releases {
		if release.IngestClusterID.String != "" {
			batch = append(batch, &pb.ActiveIngestStream{TenantId: release.TenantID, InternalName: release.StreamInternalName, ClusterId: release.IngestClusterID.String, ClaimToken: release.StartTriggerUuid, NodeId: release.NodeID, ControlCellId: localControlCellID})
		}
	}
	acknowledged := len(batch) == 0
	var releaseResult error
	if len(batch) > 0 {
		response, releaseErr := client.SyncActiveIngestPlacement(ctx, "", nil, batch)
		switch {
		case releaseErr != nil:
			releaseResult = releaseErr
		case response == nil || !response.GetExecutionFencesApplied():
			releaseResult = errors.New("publisher lease terminal fence was not confirmed")
		default:
			// The server commits every requested execution's terminal marker
			// before reporting a refused owner. Such a refusal leaves another
			// node/cell untouched, but this exact execution is settled.
			acknowledged = true
			if len(response.GetReleaseRefused()) > 0 {
				releaseResult = errors.New("publisher execution fenced while a different lease owner remains")
			}
		}
	}
	settled := make([]map[string]string, 0, len(releases))
	for _, release := range releases {
		if acknowledged || release.IngestClusterID.String == "" {
			settled = append(settled, map[string]string{"tenant_id": release.TenantID, "generation": release.Generation})
		}
	}
	if len(settled) == 0 {
		return releaseResult
	}
	payload, err := json.Marshal(settled)
	if err != nil {
		return errors.Join(releaseResult, err)
	}
	_, err = q.MarkPublisherLeasesReleased(ctx, payload)
	return errors.Join(releaseResult, err)
}

// A bounded batch keeps WAN recovery from serializing a backlog behind one
// execution. Failed items remain durable and are retried on the next pass.
func recoverPublisherBatch(ctx context.Context, count int, work func(int) error) error {
	var wg sync.WaitGroup
	var mu sync.Mutex
	var result error
	items := make(chan int)
	for range min(count, 8) {
		wg.Go(func() {
			for i := range items {
				if err := work(i); err != nil {
					mu.Lock()
					result = errors.Join(result, err)
					mu.Unlock()
				}
			}
		})
	}
	for i := range count {
		select {
		case items <- i:
		case <-ctx.Done():
			close(items)
			wg.Wait()
			return errors.Join(result, ctx.Err())
		}
	}
	close(items)
	wg.Wait()
	return result
}
