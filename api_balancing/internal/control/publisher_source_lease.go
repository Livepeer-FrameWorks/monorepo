package control

import (
	"context"
	"errors"

	"frameworks/api_balancing/internal/database/foghorndb"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

type publisherLeaseReader interface {
	GetPublisherLease(context.Context, *pb.PublisherLeaseRequest) (*pb.PublisherLeaseResponse, error)
}

// VerifyPublisherSourceLease checks both the cell session and global owner at
// physical source admission. Cached advertisements cannot extend an expired or
// replaced global lease, and this read neither acquires nor renews ownership.
func (r *StreamRegistry) VerifyPublisherSourceLease(ctx context.Context, tenant, internal, node, generation string, epoch int64) error {
	if r == nil || db == nil || epoch <= 0 || localControlCellID == "" {
		return errors.New("publisher ownership proof unavailable")
	}
	r.mu.RLock()
	reader, ok := r.client.(publisherLeaseReader)
	r.mu.RUnlock()
	if !ok || reader == nil {
		return errors.New("publisher lease verification unsupported")
	}
	binding, err := foghorndb.New(db).GetPublisherSourceLeaseBinding(ctx, foghorndb.GetPublisherSourceLeaseBindingParams{
		TenantID: tenant, Generation: generation, InternalName: internal, NodeID: node,
	})
	if err != nil {
		return err
	}
	if binding.PublisherLeaseEpoch != epoch || !binding.IngestClusterID.Valid || binding.StreamID == "" {
		return errors.New("publisher source ownership differs")
	}
	lease, err := reader.GetPublisherLease(ctx, &pb.PublisherLeaseRequest{TenantId: tenant, StreamId: binding.StreamID,
		InternalName: internal, ClusterId: binding.IngestClusterID.String, ControlCellId: localControlCellID, NodeId: node,
		ExecutionToken: binding.StartTriggerUuid})
	if err != nil {
		return err
	}
	if lease.GetOutcome() != pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_SAME_EXECUTION || lease.GetLeaseEpoch() != epoch {
		return errors.New("publisher source no longer owns its global lease")
	}
	return nil
}
