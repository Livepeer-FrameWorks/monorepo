package grpc

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	authority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	media "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func validatePublisherLeaseRequest(ctx context.Context, req *pb.PublisherLeaseRequest) error {
	if err := requireServiceAuthentication(ctx, "publisher lease"); err != nil {
		return err
	}
	for _, id := range []string{req.GetTenantId(), req.GetStreamId()} {
		if _, err := uuid.Parse(id); err != nil {
			return status.Error(codes.InvalidArgument, "publisher lease requires tenant and stream UUIDs")
		}
	}
	for _, id := range []string{req.GetInternalName(), req.GetClusterId(), req.GetControlCellId(), req.GetNodeId(), req.GetExecutionToken()} {
		if id == "" || strings.TrimSpace(id) != id || len(id) > 255 {
			return status.Error(codes.InvalidArgument, "publisher lease identity is incomplete")
		}
	}
	if len(req.GetNodeId()) > 100 || len(req.GetExecutionToken()) > 64 {
		return status.Error(codes.InvalidArgument, "publisher execution identity too long")
	}
	if req.GetTenantAuthorityVersion() <= 0 || req.GetObjectAuthorityVersion() <= 0 || len(req.GetTenantPayloadSha256()) != 32 || len(req.GetObjectPayloadSha256()) != 32 {
		return status.Error(codes.InvalidArgument, "publisher lease requires an exact authority pair")
	}
	return nil
}

// AcquirePublisherLease serializes the lease against current publication changes.
// Permission comes from Commodore's own published pair, never caller configuration.
func (s *CommodoreServer) AcquirePublisherLease(ctx context.Context, req *pb.PublisherLeaseRequest) (*pb.PublisherLeaseResponse, error) {
	if err := validatePublisherLeaseRequest(ctx, req); err != nil {
		return nil, err
	}
	var response *pb.PublisherLeaseResponse
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		response = &pb.PublisherLeaseResponse{Outcome: pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_AUTHORITY_STALE}
		q := commodoredb.New(tx)
		if err := q.LockPublisherLeaseExecution(ctx, publisherLeaseExecutionKey(req.GetControlCellId(), req.GetNodeId(), req.GetExecutionToken())); err != nil {
			return err
		}
		abandoned, err := q.PublisherLeaseAbandoned(ctx, commodoredb.PublisherLeaseAbandonedParams{ControlCellID: req.GetControlCellId(), NodeID: req.GetNodeId(), ExecutionToken: req.GetExecutionToken()})
		if err != nil {
			return err
		}
		if abandoned {
			response.Outcome = pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_DENIED
			return nil
		}
		pair, err := q.LockPublisherLeaseAuthority(ctx, commodoredb.LockPublisherLeaseAuthorityParams{TenantID: req.GetTenantId(), AuthorityID: authority.LiveStreamAuthorityID(req.GetStreamId()), CellID: req.GetControlCellId()})
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		now := time.Now()
		if pair.ObjectVersion != req.GetObjectAuthorityVersion() || pair.TenantVersion != req.GetTenantAuthorityVersion() || !bytes.Equal(pair.ObjectDigest, req.GetObjectPayloadSha256()) || !bytes.Equal(pair.TenantDigest, req.GetTenantPayloadSha256()) || !now.Before(pair.ObjectValidUntil) || !now.Before(pair.TenantValidUntil) {
			return nil
		}
		object, tenant := &media.MediaObjectAuthority{}, &media.TenantAuthority{}
		if err = proto.Unmarshal(pair.ObjectPayload, object); err != nil {
			return err
		}
		if err = proto.Unmarshal(pair.TenantPayload, tenant); err != nil {
			return err
		}
		response.Outcome = pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_DENIED
		if object.GetTenantId() != req.GetTenantId() || tenant.GetTenantId() != req.GetTenantId() || object.GetLiveStream().GetStreamId() != req.GetStreamId() || object.GetInternalName() != req.GetInternalName() || object.GetLiveStream().GetIngestMode() != "push" || object.GetLifecycle() != media.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE || tenant.GetLifecycle() != media.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE || tenant.GetBillingDecision() != media.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW {
			return nil
		}
		entitled := false
		for _, grant := range tenant.GetEffectiveClusterGrants() {
			if grant.GetClusterId() != req.GetClusterId() || grant.GetControlCellId() != req.GetControlCellId() {
				continue
			}
			if expiry := grant.GetExpiresAt(); expiry != nil && !now.Before(expiry.AsTime()) {
				continue
			}
			if grant.GetClusterType() != "edge" {
				continue
			}
			if authority.IsPlacementSchema(tenant.GetSchemaVersion()) && !grant.GetMediaConsent().GetAllowIngest() {
				continue
			}
			entitled = true
		}
		if !entitled {
			return nil
		}
		claim, err := q.AcquirePublisherLease(ctx, commodoredb.AcquirePublisherLeaseParams{TenantID: req.GetTenantId(), StreamID: req.GetStreamId(), InternalName: req.GetInternalName(), NodeID: sql.NullString{String: req.GetNodeId(), Valid: true}, ControlCellID: sql.NullString{String: req.GetControlCellId(), Valid: true}, ClusterID: sql.NullString{String: req.GetClusterId(), Valid: true}, ExecutionToken: sql.NullString{String: req.GetExecutionToken(), Valid: true}, LeaseSeconds: int64(activeIngestLease.Seconds())})
		if errors.Is(err, sql.ErrNoRows) {
			held, readErr := q.GetPublisherLease(ctx, commodoredb.GetPublisherLeaseParams{TenantID: req.GetTenantId(), StreamID: req.GetStreamId(), InternalName: req.GetInternalName(), LeaseSeconds: int64(activeIngestLease.Seconds())})
			if errors.Is(readErr, sql.ErrNoRows) {
				return nil
			}
			if readErr != nil {
				return readErr
			}
			response.Outcome = pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_HELD_BY_OTHER
			response.ExpiresAt = timestamppb.New(held.ExpiresAt)
			if held.ActiveIngestClusterID.String == req.GetClusterId() {
				response.HeldExecutionToken = held.ActiveIngestClaimID.String
			}
			return nil
		}
		if err != nil {
			return err
		}
		response.Outcome = pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_ACQUIRED
		if claim.PrevFresh {
			response.Outcome = pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_SAME_EXECUTION
		}
		response.ExpiresAt = timestamppb.New(claim.ExpiresAt)
		return nil
	})
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "publisher lease unavailable: %v", err)
	}
	return response, nil
}

func (s *CommodoreServer) GetPublisherLease(ctx context.Context, req *pb.PublisherLeaseRequest) (*pb.PublisherLeaseResponse, error) {
	if err := validatePublisherLeaseRequest(ctx, req); err != nil {
		return nil, err
	}
	held, err := commodoredb.New(s.db).GetPublisherLease(ctx, commodoredb.GetPublisherLeaseParams{TenantID: req.GetTenantId(), StreamID: req.GetStreamId(), InternalName: req.GetInternalName(), LeaseSeconds: int64(activeIngestLease.Seconds())})
	if errors.Is(err, sql.ErrNoRows) {
		return &pb.PublisherLeaseResponse{Outcome: pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_DENIED}, nil
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "publisher lease lookup unavailable")
	}
	response := &pb.PublisherLeaseResponse{Outcome: pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_HELD_BY_OTHER, ExpiresAt: timestamppb.New(held.ExpiresAt)}
	if !time.Now().Before(held.ExpiresAt) || held.ActiveIngestClusterID.String == "" {
		response.Outcome = pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_UNSPECIFIED
	} else if held.ActiveIngestClusterID.String == req.GetClusterId() {
		response.HeldExecutionToken = held.ActiveIngestClaimID.String
		if response.HeldExecutionToken == req.GetExecutionToken() {
			response.Outcome = pb.PublisherLeaseOutcome_PUBLISHER_LEASE_OUTCOME_SAME_EXECUTION
		}
	}
	return response, nil
}

// RenewPublisherLeases exposes renewal separately from initial acquisition.
func (s *CommodoreServer) RenewPublisherLeases(ctx context.Context, req *pb.SyncActiveIngestPlacementRequest) (*pb.SyncActiveIngestPlacementResponse, error) {
	if len(req.GetRelease()) > 0 {
		return nil, status.Error(codes.InvalidArgument, "lease renewal cannot release")
	}
	strict := proto.CloneOf(req)
	for _, entry := range strict.GetRenew() {
		entry.RequireExistingOwner = true
	}
	return s.SyncActiveIngestPlacement(ctx, strict)
}

func publisherLeaseExecutionKey(cell, node, execution string) string {
	return fmt.Sprintf("publisher:%d:%s:%d:%s:%d:%s", len(cell), cell, len(node), node, len(execution), execution)
}

// AbandonPublisherLease records the definitive execution outcome even when no
// cell-side generation was committed or its preparation reply was lost.
func (s *CommodoreServer) AbandonPublisherLease(ctx context.Context, req *pb.AbandonPublisherLeaseRequest) (*emptypb.Empty, error) {
	if err := requireServiceAuthentication(ctx, "publisher lease abandonment"); err != nil {
		return nil, err
	}
	for _, id := range []string{req.GetControlCellId(), req.GetNodeId(), req.GetExecutionToken()} {
		if id == "" || strings.TrimSpace(id) != id || len(id) > 255 {
			return nil, status.Error(codes.InvalidArgument, "publisher execution identity required")
		}
	}
	if len(req.GetNodeId()) > 100 || len(req.GetExecutionToken()) > 64 {
		return nil, status.Error(codes.InvalidArgument, "publisher execution identity too long")
	}
	err := database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		q := commodoredb.New(tx)
		if err := q.LockPublisherLeaseExecution(ctx, publisherLeaseExecutionKey(req.GetControlCellId(), req.GetNodeId(), req.GetExecutionToken())); err != nil {
			return err
		}
		if err := q.RecordPublisherLeaseAbandonment(ctx, commodoredb.RecordPublisherLeaseAbandonmentParams{ControlCellID: req.GetControlCellId(), NodeID: req.GetNodeId(), ExecutionToken: req.GetExecutionToken()}); err != nil {
			return err
		}
		_, err := q.ReleaseAbandonedPublisherLease(ctx, commodoredb.ReleaseAbandonedPublisherLeaseParams{ControlCellID: sql.NullString{String: req.GetControlCellId(), Valid: true}, NodeID: sql.NullString{String: req.GetNodeId(), Valid: true}, ExecutionToken: sql.NullString{String: req.GetExecutionToken(), Valid: true}})
		return err
	})
	if err != nil {
		return nil, status.Errorf(codes.Unavailable, "publisher abandonment unavailable: %v", err)
	}
	return &emptypb.Empty{}, nil
}

var errPublisherExecutionAbandoned = errors.New("publisher execution abandoned")

func validatePublisherExecutionIdentity(nodeID, cellID string) error {
	if nodeID == "" || strings.TrimSpace(nodeID) != nodeID || len(nodeID) > 100 || cellID == "" || strings.TrimSpace(cellID) != cellID || len(cellID) > 255 {
		return status.Error(codes.InvalidArgument, "publisher node and cell identity required together")
	}
	return nil
}

// acquireCompatiblePublisherClaim binds the configuration-building cold path
// to the same definitive execution fence as the published-authority fast path.
func (s *CommodoreServer) acquireCompatiblePublisherClaim(ctx context.Context, req *pb.ValidateStreamKeyRequest, clusterID string) (claim commodoredb.AcquireIngestClaimRow, err error) {
	bound := req.GetNodeId() != "" || req.GetControlCellId() != ""
	token := strings.TrimSpace(req.GetClaimToken())
	params := commodoredb.AcquireIngestClaimParams{ClusterID: sql.NullString{String: clusterID, Valid: true}, ClaimToken: sql.NullString{String: token, Valid: true}, LeaseSeconds: int64(activeIngestLease.Seconds()), LookupStreamKey: req.GetStreamKey()}
	if !bound {
		return commodoredb.New(s.db).AcquireIngestClaim(ctx, params)
	}
	if err = validatePublisherExecutionIdentity(req.GetNodeId(), req.GetControlCellId()); err != nil {
		return claim, err
	}
	params.NodeID = sql.NullString{String: req.GetNodeId(), Valid: true}
	params.ControlCellID = sql.NullString{String: req.GetControlCellId(), Valid: true}
	err = database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
		q := commodoredb.New(tx)
		if lockErr := q.LockPublisherLeaseExecution(ctx, publisherLeaseExecutionKey(req.GetControlCellId(), req.GetNodeId(), token)); lockErr != nil {
			return lockErr
		}
		abandoned, readErr := q.PublisherLeaseAbandoned(ctx, commodoredb.PublisherLeaseAbandonedParams{ControlCellID: req.GetControlCellId(), NodeID: req.GetNodeId(), ExecutionToken: token})
		if readErr != nil {
			return readErr
		}
		if abandoned {
			return errPublisherExecutionAbandoned
		}
		var claimErr error
		claim, claimErr = q.AcquireIngestClaim(ctx, params)
		return claimErr
	})
	return claim, err
}
