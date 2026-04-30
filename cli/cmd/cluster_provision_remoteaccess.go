package cmd

import (
	"context"
	"fmt"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/remoteaccess"
	"frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"
)

// resolveServiceDial returns the dial address, TLS server name, and insecure
// flag a gRPC client should use for serviceName. When sess is non-nil the
// dial address is a loopback endpoint backed by an SSH local-forward and
// serverName is the cert-bearing hostname for SNI/verification. When sess
// is nil the dial address is the mesh address (or external IP fallback) and
// serverName is empty — gRPC then verifies against the dial address's host.
func resolveServiceDial(
	ctx context.Context,
	manifest *inventory.Manifest,
	sess *remoteaccess.Session,
	serviceName string,
	defaultGRPCPort int,
) (dialAddr, serverName string, insecure bool, err error) {
	if sess != nil {
		ep, epErr := sess.Endpoint(ctx, remoteaccess.ServiceTarget{
			Name:            serviceName,
			DefaultGRPCPort: defaultGRPCPort,
		})
		if epErr != nil {
			return "", "", false, fmt.Errorf("remoteaccess endpoint for %s: %w", serviceName, epErr)
		}
		return ep.DialAddr, ep.ServerName, ep.Insecure, nil
	}

	addr, addrErr := resolveServiceGRPCAddr(manifest, serviceName, defaultGRPCPort)
	if addrErr != nil {
		return "", "", false, addrErr
	}
	return addr, "", isDevProfile(manifest), nil
}

// quartermasterDialEndpoint resolves the full dial tuple for Quartermaster.
// Token comes from runtimeData (seeded earlier from the manifest env files).
func quartermasterDialEndpoint(
	ctx context.Context,
	manifest *inventory.Manifest,
	runtimeData map[string]any,
	sess *remoteaccess.Session,
) (serviceToken, dialAddr, serverName string, insecure bool, err error) {
	token, _, err := resolveQuartermasterRuntimeData(manifest, runtimeData)
	if err != nil {
		return "", "", "", false, err
	}
	addr, name, ins, err := resolveServiceDial(ctx, manifest, sess, "quartermaster", defaultGRPCPort("quartermaster"))
	if err != nil {
		return "", "", "", false, err
	}
	return token, addr, name, ins, nil
}

// internalCAFromRuntime returns the bootstrap CA bundle to set on a service
// gRPC client config when one is staged in runtimeData. Empty otherwise.
func internalCAFromRuntime(runtimeData map[string]any) string {
	if pki, ok := runtimeData["internal_pki_bootstrap"].(*internalPKIBootstrap); ok && pki != nil {
		return pki.CABundlePEM
	}
	return ""
}

// newQuartermasterClient builds a Quartermaster gRPC client. When sess is
// non-nil the call is routed through an SSH local-forward; otherwise it
// dials the manifest mesh/external IP directly.
//
// The dial address used for the underlying gRPC connection is intentionally
// not exposed: when tunneled it's a 127.0.0.1:<local> loopback that is only
// meaningful on the operator host, and leaking it into runtimeData (and from
// there into per-task ServiceConfig.Metadata) would ship a useless or
// misleading value to every target node. Callers that need to record
// "Quartermaster's address for cluster consumers" should resolve it
// independently via resolveServiceGRPCAddr against the manifest.
func newQuartermasterClient(
	ctx context.Context,
	manifest *inventory.Manifest,
	runtimeData map[string]any,
	sess *remoteaccess.Session,
) (*quartermaster.GRPCClient, error) {
	token, addr, serverName, insecure, err := quartermasterDialEndpoint(ctx, manifest, runtimeData, sess)
	if err != nil {
		return nil, err
	}
	cfg := quartermaster.GRPCConfig{
		GRPCAddr:      addr,
		Logger:        logging.NewLogger(),
		ServiceToken:  token,
		AllowInsecure: insecure,
		ServerName:    serverName,
		CACertPEM:     internalCAFromRuntime(runtimeData),
	}
	return quartermaster.NewGRPCClient(cfg)
}
