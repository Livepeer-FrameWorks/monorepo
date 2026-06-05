package grpc

import (
	"context"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	foghornrelaypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_relay"

	grpclib "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// RelayServer implements FoghornRelay for intra-cluster HA command forwarding.
// When a peer Foghorn instance needs to push a command to a node connected to
// this instance, it calls ForwardCommand. The handler dispatches to the local
// sendLocal* function, preventing relay loops.
type RelayServer struct {
	foghornrelaypb.UnimplementedFoghornRelayServer
	logger logging.Logger
}

// NewRelayServer creates a FoghornRelay gRPC handler.
func NewRelayServer(logger logging.Logger) *RelayServer {
	return &RelayServer{logger: logger}
}

// RegisterServices registers the FoghornRelay service on the gRPC server.
func (s *RelayServer) RegisterServices(srv *grpclib.Server) {
	foghornrelaypb.RegisterFoghornRelayServer(srv, s)
}

// ForwardCommand dispatches a relayed control command to the local node connection.
func (s *RelayServer) ForwardCommand(ctx context.Context, req *foghornrelaypb.ForwardCommandRequest) (*foghornrelaypb.ForwardCommandResponse, error) {
	if req.TargetNodeId == "" {
		return nil, status.Error(codes.InvalidArgument, "target_node_id required")
	}

	logFields := logging.Fields{
		"node_id":         req.TargetNodeId,
		"command_type":    control.RelayCommandType(req),
		"request_id":      control.RelayRequestID(req),
		"source_instance": "",
		"source_peer":     "",
	}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if vals := md.Get("x-foghorn-instance-id"); len(vals) > 0 {
			logFields["source_instance"] = vals[0]
		}
	}
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		logFields["source_peer"] = p.Addr.String()
	}
	log := s.logger.WithFields(logFields)

	var err error
	switch cmd := req.Command.(type) {
	case *foghornrelaypb.ForwardCommandRequest_ConfigSeed:
		err = control.SendLocalConfigSeed(req.TargetNodeId, cmd.ConfigSeed)
	case *foghornrelaypb.ForwardCommandRequest_DvrStart:
		err = control.SendLocalDVRStart(req.TargetNodeId, cmd.DvrStart)
	case *foghornrelaypb.ForwardCommandRequest_DvrStop:
		err = control.SendLocalDVRStop(req.TargetNodeId, cmd.DvrStop)
	case *foghornrelaypb.ForwardCommandRequest_ClipDelete:
		err = control.SendLocalClipDelete(req.TargetNodeId, cmd.ClipDelete)
	case *foghornrelaypb.ForwardCommandRequest_DvrDelete:
		err = control.SendLocalDVRDelete(req.TargetNodeId, cmd.DvrDelete)
	case *foghornrelaypb.ForwardCommandRequest_VodDelete:
		err = control.SendLocalVodDelete(req.TargetNodeId, cmd.VodDelete)
	case *foghornrelaypb.ForwardCommandRequest_DtshSync:
		err = control.SendLocalDtshSyncRequest(req.TargetNodeId, cmd.DtshSync)
	case *foghornrelaypb.ForwardCommandRequest_StopSessions:
		err = control.SendLocalStopSessions(req.TargetNodeId, cmd.StopSessions)
	case *foghornrelaypb.ForwardCommandRequest_InvalidateSessions:
		err = control.SendLocalInvalidateSessions(req.TargetNodeId, cmd.InvalidateSessions)
	case *foghornrelaypb.ForwardCommandRequest_ActivatePushTargets:
		err = control.SendLocalActivatePushTargets(req.TargetNodeId, cmd.ActivatePushTargets)
	case *foghornrelaypb.ForwardCommandRequest_DeactivatePushTargets:
		err = control.SendLocalDeactivatePushTargets(req.TargetNodeId, cmd.DeactivatePushTargets)
	case *foghornrelaypb.ForwardCommandRequest_ProcessingJob:
		err = control.SendLocalProcessingJob(req.TargetNodeId, cmd.ProcessingJob)
	case *foghornrelaypb.ForwardCommandRequest_Freeze:
		err = control.SendLocalFreezeRequest(req.TargetNodeId, cmd.Freeze)
	case *foghornrelaypb.ForwardCommandRequest_DesiredStateUpdate:
		err = control.SendLocalDesiredStateUpdate(req.TargetNodeId, cmd.DesiredStateUpdate)
	case *foghornrelaypb.ForwardCommandRequest_ApplyManagedStream:
		err = control.SendLocalApplyManagedStream(req.TargetNodeId, cmd.ApplyManagedStream)
	case *foghornrelaypb.ForwardCommandRequest_RetractManagedStream:
		err = control.SendLocalRetractManagedStream(req.TargetNodeId, cmd.RetractManagedStream)
	case *foghornrelaypb.ForwardCommandRequest_DrainStream:
		err = control.SendLocalDrainStream(req.TargetNodeId, cmd.DrainStream)
	case *foghornrelaypb.ForwardCommandRequest_DvrUpdateSource:
		err = control.SendLocalDVRUpdateSource(req.TargetNodeId, cmd.DvrUpdateSource)
	default:
		return nil, status.Error(codes.InvalidArgument, "unknown command type")
	}

	if err != nil {
		log.WithError(err).Warn("Relay local dispatch failed")
		return &foghornrelaypb.ForwardCommandResponse{Delivered: false, Error: err.Error()}, nil
	}
	log.Debug("Relay local dispatch delivered")
	return &foghornrelaypb.ForwardCommandResponse{Delivered: true}, nil
}
