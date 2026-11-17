package control

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"

	"frameworks/api_balancing/internal/database/foghorndb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

var ErrPublisherAuthorityStale = errors.New("published ingest authority requires refresh")

type PublisherLeaseClient interface {
	AcquirePublisherLease(context.Context, *pb.PublisherLeaseRequest) (*pb.PublisherLeaseResponse, error)
	SyncActiveIngestPlacement(context.Context, string, []*pb.ActiveIngestStream, []*pb.ActiveIngestStream) (*pb.SyncActiveIngestPlacementResponse, error)
}

type publishedClaimClient struct {
	PublisherLeaseClient
	request            *pb.PublisherLeaseRequest
	identity           *pb.ValidateStreamKeyResponse
	refresh            PublisherAuthorityRefresh
	authorityExchanged bool
}

// PublisherAuthorityRefresh verifies and persists a returned pair, revalidates
// the presented credential and rebuilds configuration before another exact CAS.
type PublisherAuthorityRefresh func(context.Context, [][]byte) (*pb.PublisherLeaseRequest, *pb.ValidateStreamKeyResponse, error)

func (c *publishedClaimClient) ValidateStreamKeyForClaim(ctx context.Context, _ string, clusterID, token string) (*pb.ValidateStreamKeyResponse, error) {
	req := proto.CloneOf(c.request)
	req.ClusterId, req.ExecutionToken = clusterID, token
	var lease *pb.PublisherLeaseResponse
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var err error
		lease, err = c.AcquirePublisherLease(ctx, req)
		if err != nil {
			return nil, err
		}
		if lease == nil {
			return nil, errors.New("publisher lease response missing")
		}
		if lease.GetOutcome() != pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_AUTHORITY_STALE {
			break
		}
		if c.authorityExchanged || c.refresh == nil || len(lease.GetSignedAuthorities()) != 2 {
			return nil, ErrPublisherAuthorityStale
		}
		c.authorityExchanged = true
		current, identity, refreshErr := c.refresh(ctx, lease.GetSignedAuthorities())
		if refreshErr != nil {
			return nil, fmt.Errorf("refresh publisher authority: %w", refreshErr)
		}
		if identity != nil && !identity.GetValid() {
			denial := proto.CloneOf(identity)
			denial.ClaimAcquired = false
			return denial, nil
		}
		if identity == nil || !publisherAuthorityExchangeMatches(req, current) || identity.GetTenantId() != current.GetTenantId() || identity.GetStreamId() != current.GetStreamId() || identity.GetInternalName() != current.GetInternalName() {
			return nil, ErrPublisherAuthorityStale
		}
		req = proto.CloneOf(current)
		c.request, c.identity = proto.CloneOf(current), proto.CloneOf(identity)
	}
	response := proto.CloneOf(c.identity)
	response.ClaimAcquired = false
	switch lease.GetOutcome() {
	case pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_ACQUIRED:
		response.ClaimAcquired = true
	case pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_SAME_EXECUTION:
	case pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_HELD_BY_OTHER:
		response.Valid = false
		response.RejectionReason = pb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_DUPLICATE_INGEST
		response.Error = "another publisher holds this stream"
		response.HeldClaimToken = lease.GetHeldExecutionToken()
	case pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_AUTHORITY_STALE:
		return nil, ErrPublisherAuthorityStale
	default:
		response.Valid = false
		response.Error = "publisher lease denied"
		response.RejectionReason = pb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_UNSPECIFIED
	}
	return response, nil
}

func publisherAuthorityExchangeMatches(before, after *pb.PublisherLeaseRequest) bool {
	if after == nil || before.GetTenantId() != after.GetTenantId() || before.GetStreamId() != after.GetStreamId() || before.GetInternalName() != after.GetInternalName() || before.GetNodeId() != after.GetNodeId() || before.GetControlCellId() != after.GetControlCellId() || before.GetClusterId() != after.GetClusterId() || before.GetExecutionToken() != after.GetExecutionToken() {
		return false
	}
	if after.GetTenantAuthorityVersion() < before.GetTenantAuthorityVersion() || after.GetObjectAuthorityVersion() < before.GetObjectAuthorityVersion() || len(after.GetTenantPayloadSha256()) != 32 || len(after.GetObjectPayloadSha256()) != 32 {
		return false
	}
	if (after.GetTenantAuthorityVersion() == before.GetTenantAuthorityVersion() && !bytes.Equal(before.GetTenantPayloadSha256(), after.GetTenantPayloadSha256())) || (after.GetObjectAuthorityVersion() == before.GetObjectAuthorityVersion() && !bytes.Equal(before.GetObjectPayloadSha256(), after.GetObjectPayloadSha256())) {
		return false
	}
	return after.GetTenantAuthorityVersion() != before.GetTenantAuthorityVersion() || after.GetObjectAuthorityVersion() != before.GetObjectAuthorityVersion()
}

// ClaimPublishedIngestPlacement retains ended-owner recovery while the normal
// path performs exactly one lease mutation and no configuration validation.
func ClaimPublishedIngestPlacement(ctx context.Context, client PublisherLeaseClient, request *pb.PublisherLeaseRequest, identity *pb.ValidateStreamKeyResponse, nodeID string, logger logging.Logger, refresh ...PublisherAuthorityRefresh) (*pb.ValidateStreamKeyResponse, error) {
	adapter := &publishedClaimClient{PublisherLeaseClient: client, request: request, identity: identity}
	if len(refresh) == 1 {
		adapter.refresh = refresh[0]
	}
	return ClaimIngestPlacement(ctx, adapter, "", request.GetTenantId(), request.GetInternalName(), nodeID, request.GetClusterId(), request.GetExecutionToken(), logger)
}

type executedClaimClient struct {
	IngestClaimClient
	claim          func(context.Context, string, string, string, string, string) (*pb.ValidateStreamKeyResponse, error)
	nodeID, cellID string
}

func (c executedClaimClient) ValidateStreamKeyForClaim(ctx context.Context, key, cluster, token string) (*pb.ValidateStreamKeyResponse, error) {
	return c.claim(ctx, key, cluster, token, c.nodeID, c.cellID)
}

// ClaimExecutedIngestPlacement retains connected configuration discovery while
// its lease participates in definitive node-execution abandonment.
func ClaimExecutedIngestPlacement(ctx context.Context, client IngestClaimClient, key, tenant, internal, node, cluster, token, cell string, logger logging.Logger) (*pb.ValidateStreamKeyResponse, error) {
	fenced, ok := client.(interface {
		ValidateStreamKeyForPublisherClaim(context.Context, string, string, string, string, string) (*pb.ValidateStreamKeyResponse, error)
	})
	if !ok {
		return nil, status.Error(codes.Unimplemented, "publisher execution claim capability unavailable")
	}
	return ClaimIngestPlacement(ctx, executedClaimClient{IngestClaimClient: client, claim: fenced.ValidateStreamKeyForPublisherClaim, nodeID: node, cellID: cell}, key, tenant, internal, node, cluster, token, logger)
}

type ingestPlacementSyncClient interface {
	SyncActiveIngestPlacement(context.Context, string, []*pb.ActiveIngestStream, []*pb.ActiveIngestStream) (*pb.SyncActiveIngestPlacementResponse, error)
}

// The generic compatibility coordinator proves the held token ended before
// releasing it. New adapters add the ended v1 owner's identity so a queued
// outage adoption cannot recreate that execution between release and retry.
func releaseEndedPublisherClaims(ctx context.Context, client ingestPlacementSyncClient, cellID, clusterID string, renew, releases []*pb.ActiveIngestStream) (*pb.SyncActiveIngestPlacementResponse, error) {
	if len(releases) == 0 {
		return client.SyncActiveIngestPlacement(ctx, clusterID, renew, releases)
	}
	if db == nil {
		return nil, errors.New("ended publisher execution database unavailable")
	}
	fenced := make([]*pb.ActiveIngestStream, 0, len(releases))
	for _, release := range releases {
		nodes, err := foghorndb.New(db).ListEndedPublisherClaimNodes(ctx, foghorndb.ListEndedPublisherClaimNodesParams{TenantID: release.GetTenantId(), InternalName: release.GetInternalName(), ClusterID: sql.NullString{String: release.GetClusterId(), Valid: true}, ClaimToken: release.GetClaimToken()})
		if err != nil {
			return nil, fmt.Errorf("read ended publisher execution: %w", err)
		}
		if len(nodes) == 0 {
			fenced = append(fenced, proto.CloneOf(release))
			continue
		}
		if cellID == "" {
			return nil, errors.New("ended publisher execution cell unavailable")
		}
		for _, node := range nodes {
			entry := proto.CloneOf(release)
			entry.NodeId, entry.ControlCellId = node, cellID
			fenced = append(fenced, entry)
		}
	}
	return client.SyncActiveIngestPlacement(ctx, clusterID, renew, fenced)
}

func (c *publishedClaimClient) SyncActiveIngestPlacement(ctx context.Context, clusterID string, renew, releases []*pb.ActiveIngestStream) (*pb.SyncActiveIngestPlacementResponse, error) {
	return releaseEndedPublisherClaims(ctx, c.PublisherLeaseClient, c.request.GetControlCellId(), clusterID, renew, releases)
}

func (c executedClaimClient) SyncActiveIngestPlacement(ctx context.Context, clusterID string, renew, releases []*pb.ActiveIngestStream) (*pb.SyncActiveIngestPlacementResponse, error) {
	return releaseEndedPublisherClaims(ctx, c.IngestClaimClient, c.cellID, clusterID, renew, releases)
}
