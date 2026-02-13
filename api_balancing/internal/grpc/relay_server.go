package grpc

import (
	"context"

	"frameworks/api_balancing/internal/control"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

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
	pb.UnimplementedFoghornRelayServer
	logger logging.Logger
}

// NewRelayServer creates a FoghornRelay gRPC handler.
func NewRelayServer(logger logging.Logger) *RelayServer {
	return &RelayServer{logger: logger}
}

// RegisterServices registers the FoghornRelay service on the gRPC server.
func (s *RelayServer) RegisterServices(srv *grpclib.Server) {
	pb.RegisterFoghornRelayServer(srv, s)
}

// ForwardCommand dispatches a relayed control command to the local node connection.
func (s *RelayServer) ForwardCommand(ctx context.Context, req *pb.ForwardCommandRequest) (*pb.ForwardCommandResponse, error) {
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
	case *pb.ForwardCommandRequest_ConfigSeed:
		err = control.SendLocalConfigSeed(req.TargetNodeId, cmd.ConfigSeed)
	case *pb.ForwardCommandRequest_ClipPull:
		err = control.SendLocalClipPull(req.TargetNodeId, cmd.ClipPull)
	case *pb.ForwardCommandRequest_DvrStart:
		err = control.SendLocalDVRStart(req.TargetNodeId, cmd.DvrStart)
	case *pb.ForwardCommandRequest_DvrStop:
		err = control.SendLocalDVRStop(req.TargetNodeId, cmd.DvrStop)
	case *pb.ForwardCommandRequest_ClipDelete:
		err = control.SendLocalClipDelete(req.TargetNodeId, cmd.ClipDelete)
	case *pb.ForwardCommandRequest_DvrDelete:
		err = control.SendLocalDVRDelete(req.TargetNodeId, cmd.DvrDelete)
	case *pb.ForwardCommandRequest_VodDelete:
		err = control.SendLocalVodDelete(req.TargetNodeId, cmd.VodDelete)
	case *pb.ForwardCommandRequest_Defrost:
		err = control.SendLocalDefrostRequest(req.TargetNodeId, cmd.Defrost)
	case *pb.ForwardCommandRequest_DtshSync:
		err = control.SendLocalDtshSyncRequest(req.TargetNodeId, cmd.DtshSync)
	case *pb.ForwardCommandRequest_StopSessions:
		err = control.SendLocalStopSessions(req.TargetNodeId, cmd.StopSessions)
	default:
		return nil, status.Error(codes.InvalidArgument, "unknown command type")
	}

	if err != nil {
		log.WithError(err).Warn("Relay local dispatch failed")
		return &pb.ForwardCommandResponse{Delivered: false, Error: err.Error()}, nil
	}
	log.Debug("Relay local dispatch delivered")
	return &pb.ForwardCommandResponse{Delivered: true}, nil
}
