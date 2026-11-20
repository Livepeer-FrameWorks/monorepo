package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"frameworks/api_control/internal/database/commodoredb"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type ingestStopPayload struct {
	StreamID         string `json:"stream_id"`
	InternalName     string `json:"internal_name"`
	ClaimID          string `json:"claim_id"`
	ControlCellID    string `json:"control_cell_id"`
	RefreshAuthority bool   `json:"refresh_authority"`
}

func enqueueIngestStop(ctx context.Context, queries *commodoredb.Queries, tenant string, target commodoredb.LockIngestStopTargetRow, refreshAuthority bool) (bool, error) {
	if target.IngestMode != "push" {
		return false, ingestStopModeError(target.IngestMode)
	}
	if !target.ActiveIngestClaimID.Valid || strings.TrimSpace(target.ActiveIngestClaimID.String) == "" {
		return false, nil
	}
	if target.ControlCellID == "" {
		return false, status.Error(codes.Unavailable, "active ingest owner is unavailable")
	}
	payload, err := json.Marshal(ingestStopPayload{StreamID: target.ID, InternalName: target.InternalName, ClaimID: target.ActiveIngestClaimID.String, ControlCellID: target.ControlCellID, RefreshAuthority: refreshAuthority})
	if err != nil {
		return false, err
	}
	reason := "requested"
	if refreshAuthority {
		reason = "key_rotated"
	}
	requestID := uuid.NewString()
	if err := queries.EnqueueIngestStopObligation(ctx, commodoredb.EnqueueIngestStopObligationParams{TargetKey: "ingest_stop:" + requestID, TenantID: tenant, Reason: reason, SourceEventID: requestID, Payload: payload}); err != nil {
		return false, txStatus(status.Error(codes.Internal, "failed to record ingest stop"), err)
	}
	return true, nil
}

func ingestStopModeError(mode string) error {
	if mode == "pull" {
		return status.Error(codes.FailedPrecondition, "to stop a pull stream set pullSource.enabled=false")
	}
	return status.Error(codes.FailedPrecondition, "ingest disconnect is available only for push streams")
}

func (s *CommodoreServer) DisconnectIngest(ctx context.Context, request *pb.DisconnectIngestRequest) (*pb.DisconnectIngestResponse, error) {
	user, tenant, err := extractUserContext(ctx)
	if err != nil {
		return nil, err
	}
	if err = requireTenantStreamPermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
	if _, err = uuid.Parse(request.GetStreamId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "stream_id must be a UUID")
	}
	mode, err := commodoredb.New(s.db).GetStreamIngestModeForUser(ctx, commodoredb.GetStreamIngestModeForUserParams{ID: request.GetStreamId(), UserID: user, TenantID: tenant})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, status.Error(codes.NotFound, "stream not found")
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "stream catalog unavailable")
	}
	if mode != "push" {
		return nil, ingestStopModeError(mode)
	}
	var response *pb.DisconnectIngestResponse
	err = s.withStatusTx(ctx, func(error) error { return status.Error(codes.Internal, "failed to begin ingest disconnect") }, func(error) error { return status.Error(codes.Internal, "failed to commit ingest disconnect") }, func(tx *sql.Tx) error {
		response = nil
		queries := commodoredb.New(tx)
		target, readErr := queries.LockIngestStopTarget(ctx, commodoredb.LockIngestStopTargetParams{TenantID: tenant, UserID: user, StreamID: request.GetStreamId()})
		if errors.Is(readErr, sql.ErrNoRows) {
			return status.Error(codes.NotFound, "stream not found")
		}
		if readErr != nil {
			return txStatus(status.Error(codes.Internal, "failed to lock ingest owner"), readErr)
		}
		requested, enqueueErr := enqueueIngestStop(ctx, queries, tenant, target, false)
		if enqueueErr != nil {
			return enqueueErr
		}
		response = &pb.DisconnectIngestResponse{StreamId: target.ID, StopRequested: requested}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (s *CommodoreServer) processIngestStopObligations(ctx context.Context) {
	s.processMediaAuthorityObligationLane(ctx, commodoredb.MediaAuthorityLaneIngestStop, mediaAuthorityObligationLease, mediaAuthorityRefreshTimeout)
}

func (s *CommodoreServer) purgeCompletedIngestStops(ctx context.Context) {
	if _, err := commodoredb.New(s.db).PurgeCompletedIngestStops(ctx); err != nil {
		s.logger.WithError(err).Warn("Failed to purge completed ingest stops")
	}
}

func (s *CommodoreServer) applyIngestStopObligation(ctx context.Context, row commodoredb.ClaimMediaAuthorityObligationsRow) error {
	raw, err := commodoredb.New(s.db).GetIngestStopObligationPayload(ctx, commodoredb.GetIngestStopObligationPayloadParams{TenantID: row.TenantID, TargetKey: row.TargetKey})
	if err != nil {
		return err
	}
	var payload ingestStopPayload
	if err = json.Unmarshal(raw, &payload); err != nil {
		return parkAuthorityCompile("invalid_ingest_stop_payload", err)
	}
	if _, err = uuid.Parse(payload.StreamID); err != nil {
		return parkAuthorityCompile("invalid_ingest_stop_stream", err)
	}
	if payload.InternalName == "" || payload.ControlCellID == "" || strings.TrimSpace(payload.ClaimID) == "" || len(payload.ClaimID) > 64 {
		return parkAuthorityCompile("incomplete_ingest_stop_payload", errors.New("ingest stop has no exact publisher binding"))
	}
	if row.LastReason != "requested" && row.LastReason != "key_rotated" {
		return parkAuthorityCompile("invalid_ingest_stop_reason", errors.New("unknown ingest stop reason"))
	}
	if payload.RefreshAuthority {
		if !s.mediaAuthorityEnabled() {
			return errors.New("media authority compiler is not configured")
		}
		target := commodoredb.LiveStreamMediaAuthorityTarget(payload.StreamID)
		// Publish the rotated credentials before allowing the old generation's stop to complete.
		if err = s.withMediaAuthorityCompileFence(ctx, target.Key, func(compileCtx context.Context) error {
			return s.compileLiveStreamAuthority(compileCtx, payload.StreamID)
		}); err != nil {
			return err
		}
	}
	client, resolveErr := s.resolveFoghornForClusterDirect(ctx, payload.ControlCellID)
	if resolveErr != nil {
		return s.settleRetiredIngestCells(ctx, row.TenantID, []string{payload.ControlCellID}, []error{resolveErr}, []bool{true})
	}
	_, _, stopErr := client.TerminateClaimedTenantStream(ctx, row.TenantID, payload.StreamID, row.LastReason, payload.InternalName, payload.ClaimID)
	if stopErr != nil {
		return s.settleRetiredIngestCells(ctx, row.TenantID, []string{payload.ControlCellID}, []error{stopErr}, []bool{ingestCellUnreachable(stopErr)})
	}
	return nil
}

func (s *CommodoreServer) RefreshStreamKeyAndEndIngest(ctx context.Context, request *pb.RefreshStreamKeyRequest) (*pb.RefreshStreamKeyResponse, error) {
	copy := proto.CloneOf(request)
	if copy == nil {
		return nil, status.Error(codes.InvalidArgument, "stream_id is required")
	}
	copy.EndActiveSession = true
	return s.RefreshStreamKey(ctx, copy)
}
