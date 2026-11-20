package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/database/foghorndb"
	"frameworks/api_balancing/internal/domainevents"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *FoghornGRPCServer) TerminateClaimedStreamSessions(ctx context.Context, request *pb.TerminateStreamSessionsRequest) (*pb.TerminateTenantStreamsResponse, error) {
	if ctxkeys.GetAuthType(ctx) != "service" {
		return nil, status.Error(codes.PermissionDenied, "claim-fenced stops require service authentication")
	}
	if _, err := uuid.Parse(request.GetTenantId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a UUID")
	}
	if _, err := uuid.Parse(request.GetStreamId()); err != nil {
		return nil, status.Error(codes.InvalidArgument, "stream_id must be a UUID")
	}
	if len(request.GetStreamNames()) != 1 || len(request.GetClaimFences()) != 1 {
		return nil, status.Error(codes.InvalidArgument, "a claimed stop requires one stream and its exact claim")
	}
	name := request.GetStreamNames()[0]
	claim := request.GetClaimFences()[name]
	if name == "" || strings.TrimSpace(name) != name || strings.TrimSpace(claim) == "" || len(claim) > 64 {
		return nil, status.Error(codes.InvalidArgument, "stream name and exact claim are required")
	}
	reason := request.GetReason()
	if reason == "" {
		reason = "requested"
	}
	if reason != "requested" && reason != "key_rotated" {
		return nil, status.Error(codes.InvalidArgument, "invalid ingest disconnect reason")
	}
	rows, err := foghorndb.New(s.db).ListClaimedIngestStopTargets(ctx, foghorndb.ListClaimedIngestStopTargetsParams{TenantID: request.GetTenantId(), StreamID: request.GetStreamId(), InternalName: name, ClaimID: claim})
	if err != nil {
		return nil, status.Error(codes.Unavailable, "ingest session catalog unavailable")
	}
	if len(rows) > 1000 {
		return nil, status.Error(codes.ResourceExhausted, "claim matched too many session generations")
	}
	response := &pb.TerminateTenantStreamsResponse{StreamNames: []string{}}
	if len(rows) == 0 {
		return response, nil
	}
	failures := make([]error, len(rows))
	completed := make([]bool, len(rows))
	stopped := make([]bool, len(rows))
	slots := make(chan struct{}, 8)
	var workers sync.WaitGroup
	for index, row := range rows {
		workers.Go(func() {
			select {
			case slots <- struct{}{}:
			case <-ctx.Done():
				failures[index] = ctx.Err()
				return
			}
			defer func() { <-slots }()
			if row.IngestDisconnectedAt.Valid {
				completed[index] = true
				return
			}
			// A natural end settles the stop request without claiming platform causation.
			if row.EndedAt.Valid && (!row.IngestDisconnectRequestedAt.Valid || row.EndedReason != control.IngestEndedAdmissionAbandoned) {
				return
			}
			if !row.EndedAt.Valid {
				pending, fenceErr := foghorndb.New(s.db).MarkIngestDisconnectRequested(ctx, foghorndb.MarkIngestDisconnectRequestedParams{TenantID: request.GetTenantId(), SessionID: row.SessionID})
				if fenceErr != nil {
					failures[index] = fenceErr
					return
				}
				if pending > 0 {
					execution := control.IngestSessionRequest{TenantID: request.GetTenantId(), StreamID: request.GetStreamId(), InternalName: name, NodeID: row.NodeID, TriggerUUID: claim, ConnectorPID: row.ConnectorPid}
					if fenceErr := control.NewPublisherAdmissionRepository(s.db).FenceExecution(ctx, execution, row.SessionID); fenceErr != nil {
						failures[index] = fenceErr
						return
					}
					command := &ipcpb.StopSessionsRequest{TenantId: request.GetTenantId(), Reason: reason, StreamNames: []string{name}, Targets: []*ipcpb.StopSessionsTarget{{InternalName: name, SourceGeneration: row.SessionID, ConnectorPid: row.ConnectorPid, FenceAdmission: true}}}
					if stopErr := control.SendClaimedStopSessionsContext(ctx, row.NodeID, command); stopErr != nil {
						failures[index] = stopErr
						return
					}
					if settleErr := control.NewPublisherAdmissionRepository(s.db).CompleteStoppedExecution(ctx, execution, row.SessionID); settleErr != nil {
						failures[index] = settleErr
						return
					}
					stopped[index] = true
				}
			}
			failures[index] = database.WithRetryablePostgresTx(ctx, s.db, nil, func(tx *sql.Tx) error {
				affected, markErr := foghorndb.New(tx).MarkIngestDisconnected(ctx, foghorndb.MarkIngestDisconnectedParams{TenantID: request.GetTenantId(), SessionID: row.SessionID})
				if markErr != nil {
					return markErr
				}
				if affected == 0 {
					return nil
				}
				return domainevents.StreamIngestDisconnected(ctx, tx, request.GetTenantId(), request.GetStreamId(), row.SessionID, reason)
			})
			completed[index] = failures[index] == nil
		})
	}
	workers.Wait()
	if errors.Join(failures...) != nil {
		return nil, status.Error(codes.Unavailable, "claim-fenced ingest stop is still pending")
	}
	for index := range rows {
		if stopped[index] {
			response.SessionsTerminated++
		}
		if completed[index] {
			response.StreamsTerminated = 1
			response.StreamNames = []string{name}
		}
	}
	return response, nil
}
