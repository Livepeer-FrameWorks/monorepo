package cmd

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"time"

	fwcfg "frameworks/cli/internal/config"
	"frameworks/cli/pkg/clients/bridge"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
)

// bootstrapEdgeViaBridge is the canonical preregistration entry point
// for every CLI flow that holds a bootstrap token. It hands the token
// to Bridge, which validates it via Quartermaster, resolves the
// cluster's assigned Foghorn, and proxies a PreRegisterEdge call.
//
// Returns the *pb.PreRegisterEdgeResponse shape the existing call sites
// expect so the rest of the edge bootstrap pipeline (template rendering,
// cert handling, runtime Foghorn addr) is unchanged.
func bootstrapEdgeViaBridge(ctx context.Context, ctxCfg fwcfg.Context, enrollmentToken, sshTarget, sshKey, preferredNodeID, knownExternalIP string) (*foghornpb.PreRegisterEdgeResponse, error) {
	if ctxCfg.Endpoints.BridgeURL == "" {
		return nil, fmt.Errorf("bootstrap requires a Bridge URL on the active context (run 'frameworks setup' or 'frameworks context set-url bridge <url>')")
	}
	externalIP := knownExternalIP
	if externalIP == "" {
		ip, ipErr := getRemoteExternalIP(ctx, sshTarget, sshKey)
		if ipErr == nil {
			externalIP = ip
		}
		// External IP is best-effort — Foghorn can assign one if the
		// client doesn't provide it.
	}

	bc := bridge.New(ctxCfg.Endpoints.BridgeURL)
	operationCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	var out *bridge.EdgeBootstrapResult
	for {
		var err error
		out, err = bc.BootstrapEdge(operationCtx, bridge.EdgeBootstrapInput{
			Token: enrollmentToken, ExternalIP: externalIP, PreferredNodeID: preferredNodeID,
		})
		if err == nil {
			break
		}
		var pending *bridge.EdgeBootstrapPending
		if !errors.As(err, &pending) {
			return nil, err
		}
		delay := pending.RetryAfter
		if delay < time.Second {
			delay = time.Second
		}
		if delay > 5*time.Second {
			delay = 5 * time.Second
		}
		if err := waitBootstrapRetry(operationCtx, delay); err != nil {
			return nil, fmt.Errorf("bootstrap still pending for cluster %s at %s; resume that cluster: %w", pending.ClusterID, pending.Phase, err)
		}
	}
	for {
		if err := verifyBootstrapControlEndpoint(operationCtx, out.FoghornGRPCAddr, nil); err == nil {
			break
		} else if waitErr := waitBootstrapRetry(operationCtx, 2*time.Second); waitErr != nil {
			return nil, fmt.Errorf("public control identity for cluster %s is not reachable: %w: %w", out.ClusterID, err, waitErr)
		}
	}

	resp := &foghornpb.PreRegisterEdgeResponse{
		NodeId:           out.NodeID,
		EdgeDomain:       out.EdgeDomain,
		PoolDomain:       out.PoolDomain,
		ClusterSlug:      out.ClusterSlug,
		ClusterId:        out.ClusterID,
		FoghornGrpcAddr:  out.FoghornGRPCAddr,
		InternalCaBundle: []byte(out.InternalCABundle),
	}
	if t := out.Telemetry; t != nil {
		resp.Telemetry = &commonpb.EdgeTelemetryConfig{
			Enabled:     t.Enabled,
			WriteUrl:    t.WriteURL,
			BearerToken: t.BearerToken,
		}
	}
	return resp, nil
}

func waitBootstrapRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// verifyBootstrapControlEndpoint retains hostname verification while trying
// every DNS member. A failed replica must not strand an otherwise ready pool.
func verifyBootstrapControlEndpoint(ctx context.Context, addr string, config *tls.Config) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	ips, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return err
	}
	if config == nil {
		config = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	config = config.Clone()
	config.ServerName = host
	var failures []error
	for _, ip := range ips {
		dialCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		dialer := tls.Dialer{NetDialer: &net.Dialer{}, Config: config}
		conn, dialErr := dialer.DialContext(dialCtx, "tcp", net.JoinHostPort(ip, port))
		cancel()
		if dialErr == nil {
			_ = conn.Close()
			return nil
		}
		failures = append(failures, dialErr)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	if len(failures) == 0 {
		return fmt.Errorf("public control hostname has no addresses")
	}
	return errors.Join(failures...)
}
