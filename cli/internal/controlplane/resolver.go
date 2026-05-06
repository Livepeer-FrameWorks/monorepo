package controlplane

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"

	fwcfg "frameworks/cli/internal/config"
	fwgitops "frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/remoteaccess"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

type Endpoint struct {
	Address       string
	ServerName    string
	AllowInsecure bool
	cleanup       func()
}

func (e Endpoint) Cleanup() {
	if e.cleanup != nil {
		e.cleanup()
	}
}

type serviceSpec struct {
	configName  string
	manifestID  string
	defaultPort int
	localAddr   func(fwcfg.Endpoints) string
}

var grpcServices = map[string]serviceSpec{
	"commodore": {
		configName: "commodore_grpc_addr",
		manifestID: "commodore",
		localAddr:  func(ep fwcfg.Endpoints) string { return ep.CommodoreGRPCAddr },
	},
	"quartermaster": {
		configName: "quartermaster_grpc_addr",
		manifestID: "quartermaster",
		localAddr:  func(ep fwcfg.Endpoints) string { return ep.QuartermasterGRPCAddr },
	},
	"purser": {
		configName: "purser_grpc_addr",
		manifestID: "purser",
		localAddr:  func(ep fwcfg.Endpoints) string { return ep.PurserGRPCAddr },
	},
	"periscope": {
		configName: "periscope_grpc_addr",
		manifestID: "periscope-query",
		localAddr:  func(ep fwcfg.Endpoints) string { return ep.PeriscopeGRPCAddr },
	},
	"periscope-query": {
		configName: "periscope_grpc_addr",
		manifestID: "periscope-query",
		localAddr:  func(ep fwcfg.Endpoints) string { return ep.PeriscopeGRPCAddr },
	},
	"signalman": {
		configName: "signalman_grpc_addr",
		manifestID: "signalman",
		localAddr:  func(ep fwcfg.Endpoints) string { return ep.SignalmanGRPCAddr },
	},
	"decklog": {
		configName: "decklog_grpc_addr",
		manifestID: "decklog",
		localAddr:  func(ep fwcfg.Endpoints) string { return ep.DecklogGRPCAddr },
	},
	"navigator": {
		configName: "navigator_grpc_addr",
		manifestID: "navigator",
		localAddr:  func(ep fwcfg.Endpoints) string { return ep.NavigatorGRPCAddr },
	},
	"foghorn": {
		configName: "foghorn_grpc_addr",
		manifestID: "foghorn",
		localAddr:  func(ep fwcfg.Endpoints) string { return ep.FoghornGRPCAddr },
	},
}

func init() {
	for key, spec := range grpcServices {
		if spec.defaultPort == 0 {
			if port, ok := servicedefs.DefaultGRPCPort(spec.manifestID); ok {
				spec.defaultPort = port
			}
		}
		grpcServices[key] = spec
	}
}

func ResolveGRPC(ctx context.Context, ctxCfg fwcfg.Context, service string) (Endpoint, error) {
	resolver := NewResolver(ctxCfg)
	ep, err := resolver.ResolveGRPC(ctx, service)
	if err != nil {
		resolver.Close()
		return Endpoint{}, err
	}
	ep.cleanup = resolver.Close
	return ep, nil
}

type Resolver struct {
	ctxCfg          fwcfg.Context
	manifest        *inventory.Manifest
	manifestCleanup func()
	sshSession      *remoteaccess.Session
}

func NewResolver(ctxCfg fwcfg.Context) *Resolver {
	return &Resolver{ctxCfg: ctxCfg}
}

func (r *Resolver) Close() {
	if r == nil {
		return
	}
	if r.sshSession != nil {
		_ = r.sshSession.Close()
		r.sshSession = nil
	}
	if r.manifestCleanup != nil {
		r.manifestCleanup()
		r.manifestCleanup = nil
	}
	r.manifest = nil
}

func (r *Resolver) ResolveGRPC(ctx context.Context, service string) (Endpoint, error) {
	if r == nil {
		return Endpoint{}, fmt.Errorf("nil control-plane resolver")
	}
	ctxCfg := r.ctxCfg
	spec, ok := grpcServices[service]
	if !ok {
		return Endpoint{}, fmt.Errorf("unknown control-plane gRPC service %q", service)
	}

	switch ctxCfg.EffectiveAccessMode() {
	case fwcfg.AccessModeLocal:
		addr, err := fwcfg.RequireEndpoint(ctxCfg, spec.configName, spec.localAddr(ctxCfg.Endpoints), false)
		if err != nil {
			return Endpoint{}, err
		}
		return savedEndpoint(ctxCfg, addr)
	case fwcfg.AccessModeSSH:
		if ctxCfg.Persona != fwcfg.PersonaPlatform {
			return Endpoint{}, fmt.Errorf("%s requires a platform context when access_mode=ssh; current context %q has persona %q", service, ctxCfg.Name, ctxCfg.Persona)
		}
		if addr := nonLocalOverride(spec.localAddr(ctxCfg.Endpoints)); addr != "" {
			return savedEndpoint(ctxCfg, addr)
		}
		manifest, err := r.loadManifest(ctx)
		if err != nil {
			return Endpoint{}, err
		}
		if r.sshSession == nil {
			sess, sessErr := remoteaccess.OpenSession(remoteaccess.Options{
				Manifest:      manifest,
				AllowInsecure: true,
			})
			if sessErr != nil {
				return Endpoint{}, sessErr
			}
			r.sshSession = sess
		}
		ep, err := r.sshSession.Endpoint(ctx, remoteaccess.ServiceTarget{
			Name:            spec.manifestID,
			DefaultGRPCPort: spec.defaultPort,
		})
		if err != nil {
			return Endpoint{}, err
		}
		return Endpoint{
			Address:       ep.DialAddr,
			ServerName:    ep.ServerName,
			AllowInsecure: ep.Insecure,
		}, nil
	case fwcfg.AccessModeMesh:
		if ctxCfg.Persona != fwcfg.PersonaPlatform {
			return Endpoint{}, fmt.Errorf("%s requires a platform context when access_mode=mesh; current context %q has persona %q", service, ctxCfg.Name, ctxCfg.Persona)
		}
		if addr := nonLocalOverride(spec.localAddr(ctxCfg.Endpoints)); addr != "" {
			return savedEndpoint(ctxCfg, addr)
		}
		manifest, err := r.loadManifest(ctx)
		if err != nil {
			return Endpoint{}, err
		}
		addr, err := manifestMeshGRPCAddr(manifest, spec.manifestID, spec.defaultPort)
		if err != nil {
			return Endpoint{}, err
		}
		return Endpoint{Address: addr, AllowInsecure: true}, nil
	default:
		return Endpoint{}, fmt.Errorf("unsupported access_mode %q in context %q", ctxCfg.AccessMode, ctxCfg.Name)
	}
}

func (r *Resolver) loadManifest(ctx context.Context) (*inventory.Manifest, error) {
	if r.manifest != nil {
		return r.manifest, nil
	}
	manifest, cleanup, err := loadContextManifest(ctx, r.ctxCfg)
	if err != nil {
		return nil, err
	}
	r.manifest = manifest
	r.manifestCleanup = cleanup
	return manifest, nil
}

func loadContextManifest(ctx context.Context, ctxCfg fwcfg.Context) (*inventory.Manifest, func(), error) {
	cfg, err := fwcfg.Load()
	if err != nil {
		return nil, func() {}, err
	}
	rm, err := inventory.ResolveManifestSource(inventory.ResolveInput{
		Env:         fwcfg.MapEnv{},
		Context:     ctxCfg,
		GitHubCfg:   cfg.GitHub,
		GithubFetch: fwgitops.NewGithubAppFetcher(),
		Stdout:      io.Discard,
		Ctx:         ctx,
	})
	if err != nil {
		return nil, func() {}, fmt.Errorf("resolve manifest for control-plane endpoint: %w", err)
	}
	cleanup := rm.Cleanup
	if cleanup == nil {
		cleanup = func() {}
	}
	manifest, err := inventory.LoadWithHosts(rm.Path, rm.AgeKey)
	if err != nil {
		cleanup()
		return nil, func() {}, fmt.Errorf("load manifest hosts for control-plane endpoint: %w", err)
	}
	return manifest, cleanup, nil
}

func manifestMeshGRPCAddr(manifest *inventory.Manifest, serviceName string, defaultPort int) (string, error) {
	svc, ok := manifest.Services[serviceName]
	if !ok {
		return "", fmt.Errorf("%s service not found in manifest", serviceName)
	}
	hostKey := svc.Host
	if hostKey == "" && len(svc.Hosts) > 0 {
		hostKey = svc.Hosts[0]
	}
	if hostKey == "" {
		return "", fmt.Errorf("%s service has no host in manifest", serviceName)
	}
	if _, ok := manifest.GetHost(hostKey); !ok {
		return "", fmt.Errorf("%s host %q not found in manifest", serviceName, hostKey)
	}
	port := defaultPort
	if svc.GRPCPort != 0 {
		port = svc.GRPCPort
	}
	if port <= 0 {
		return "", fmt.Errorf("%s has no gRPC port", serviceName)
	}
	addr := manifest.MeshAddress(hostKey)
	if addr == "" || addr == hostKey {
		return "", fmt.Errorf("%s host %q has no WireGuard mesh address", serviceName, hostKey)
	}
	return net.JoinHostPort(addr, fmt.Sprintf("%d", port)), nil
}

func nonLocalOverride(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" || fwcfg.IsLocalhostEndpoint(addr) {
		return ""
	}
	return addr
}

func savedEndpoint(ctxCfg fwcfg.Context, addr string) (Endpoint, error) {
	if ctxCfg.Endpoints.UseTLS {
		return Endpoint{Address: addr}, nil
	}
	if ctxCfg.Endpoints.AllowInsecure || fwcfg.IsLocalhostEndpoint(addr) {
		return Endpoint{Address: addr, AllowInsecure: true}, nil
	}
	return Endpoint{}, fmt.Errorf("saved gRPC endpoint %q requires an explicit transport; run 'frameworks context set-url <service> %s --tls' or use --allow-insecure for plaintext", addr, addr)
}
