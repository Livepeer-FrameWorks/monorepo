package cmd

import (
	"context"
	"fmt"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/pkg/clients/bridge"
	pb "frameworks/pkg/proto"
)

// bootstrapEdgeViaBridge is the canonical preregistration entry point
// for every CLI flow that holds a bootstrap token. It hands the token
// to Bridge, which validates it via Quartermaster, resolves the
// cluster's assigned Foghorn, and proxies a PreRegisterEdge call.
//
// Returns the *pb.PreRegisterEdgeResponse shape the existing call sites
// expect so the rest of the edge bootstrap pipeline (template rendering,
// cert handling, runtime Foghorn addr) is unchanged.
func bootstrapEdgeViaBridge(ctx context.Context, ctxCfg fwcfg.Context, enrollmentToken, sshTarget, sshKey, preferredNodeID string) (*pb.PreRegisterEdgeResponse, error) {
	if ctxCfg.Endpoints.BridgeURL == "" {
		return nil, fmt.Errorf("bootstrap requires a Bridge URL on the active context (run 'frameworks setup' or 'frameworks context set-url bridge <url>')")
	}
	externalIP, ipErr := getRemoteExternalIP(ctx, sshTarget, sshKey)
	if ipErr != nil {
		// External IP is best-effort — Foghorn can assign one if the
		// client doesn't provide it. Surface the miss in verbose logs.
		externalIP = ""
	}

	bc := bridge.New(ctxCfg.Endpoints.BridgeURL)
	out, err := bc.BootstrapEdge(ctx, bridge.EdgeBootstrapInput{
		Token:           enrollmentToken,
		ExternalIP:      externalIP,
		PreferredNodeID: preferredNodeID,
	})
	if err != nil {
		return nil, err
	}

	resp := &pb.PreRegisterEdgeResponse{
		NodeId:           out.NodeID,
		EdgeDomain:       out.EdgeDomain,
		PoolDomain:       out.PoolDomain,
		ClusterSlug:      out.ClusterSlug,
		ClusterId:        out.ClusterID,
		FoghornGrpcAddr:  out.FoghornGRPCAddr,
		InternalCaBundle: []byte(out.InternalCABundle),
	}
	if t := out.Telemetry; t != nil {
		resp.Telemetry = &pb.EdgeTelemetryConfig{
			Enabled:     t.Enabled,
			WriteUrl:    t.WriteURL,
			BearerToken: t.BearerToken,
		}
	}
	return resp, nil
}
