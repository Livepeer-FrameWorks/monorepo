package provisioner

import (
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"frameworks/cli/internal/configschema"
	"frameworks/cli/pkg/inventory"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// composeListener is a listener a generic service opens besides its primary
// port, named by the variable that sets it. A native service binds it on the
// host directly; in docker mode the host publishes it to the container.
type composeListener struct {
	envKey string
	// bindAddr marks a host:port variable rather than a bare port.
	bindAddr bool
	// grpc falls back to the service's catalog gRPC port when neither the env
	// nor the config schema gives the port.
	grpc bool
	// loopbackBindEnv names the variable that binds the native listener to
	// 127.0.0.1. Inside a container that would refuse the published port, so
	// docker mode binds it on every container interface and publishes it on
	// host loopback only.
	loopbackBindEnv string
	// needsLeaf is a listener the service opens only with an internal-CA leaf
	// (GRPC_TLS_CERT_PATH and GRPC_TLS_KEY_PATH).
	needsLeaf bool
}

// composeListeners lists, per generic service, every listener besides its
// primary port.
var composeListeners = map[string][]composeListener{
	"commodore":        {{envKey: "GRPC_PORT", grpc: true}},
	"quartermaster":    {{envKey: "GRPC_PORT", grpc: true}},
	"purser":           {{envKey: "GRPC_PORT", grpc: true}},
	"periscope-query":  {{envKey: "GRPC_PORT", grpc: true}},
	"signalman":        {{envKey: "GRPC_PORT", grpc: true}},
	"skipper":          {{envKey: "GRPC_PORT", grpc: true}},
	"deckhand":         {{envKey: "DECKHAND_GRPC_PORT", grpc: true}},
	"navigator":        {{envKey: "NAVIGATOR_GRPC_PORT", grpc: true}},
	"lookout":          {{envKey: "LOOKOUT_GRPC_PORT", grpc: true}},
	"bosun":            {{envKey: "BOSUN_GRPC_PORT", grpc: true}},
	"decklog":          {{envKey: "DECKLOG_METRICS_PORT"}},
	"network-observer": {{envKey: "NETWORK_OBSERVER_INTERNAL_PORT", needsLeaf: true}},
	"foghorn": {
		{envKey: "FOGHORN_INTERNAL_GRPC_BIND_ADDR", bindAddr: true, grpc: true},
		{envKey: "FOGHORN_EXTERNAL_GRPC_BIND_ADDR", bindAddr: true},
		{envKey: "FOGHORN_INTERNAL_HTTP_PORT", loopbackBindEnv: "FOGHORN_INTERNAL_HTTP_BIND_ADDR"},
	},
}

// composePublishedPorts lists every port the service listens on, with the host
// addresses each is published on (none: every interface), bound as the native
// render binds them: a mesh-only service on loopback and the mesh address, a
// loopback listener on loopback. env is the process env; the returned env holds
// the bind overrides docker mode needs.
func composePublishedPorts(serviceName string, host inventory.Host, port, containerPort int, env map[string]string) ([]map[string]any, map[string]string, error) {
	meshOnly := composePublishAddrs(serviceName, host)
	ports := []map[string]any{{"port": port, "container_port": containerPort, "addrs": meshOnly}}
	overrides := map[string]string{}
	seen := map[int]bool{port: true}
	hasLeaf := strings.TrimSpace(env["GRPC_TLS_CERT_PATH"]) != "" && strings.TrimSpace(env["GRPC_TLS_KEY_PATH"]) != ""
	for _, listener := range composeListeners[serviceName] {
		if listener.needsLeaf && !hasLeaf {
			continue
		}
		listenerPort, err := composeListenerPort(serviceName, listener, env)
		if err != nil {
			return nil, nil, err
		}
		if seen[listenerPort] {
			continue
		}
		seen[listenerPort] = true
		addrs := meshOnly
		if listener.loopbackBindEnv != "" {
			addrs = []string{"127.0.0.1"}
			overrides[listener.loopbackBindEnv] = "0.0.0.0"
		}
		ports = append(ports, map[string]any{"port": listenerPort, "container_port": listenerPort, "addrs": addrs})
	}
	return ports, overrides, nil
}

// composeListenerPort is the port the service binds for listener: the env
// value, else the config schema default, else the catalog gRPC port.
func composeListenerPort(serviceName string, listener composeListener, env map[string]string) (int, error) {
	value := strings.TrimSpace(env[listener.envKey])
	if value == "" {
		if schema, err := configschema.Load(); err == nil {
			value, _ = schema.Default(serviceName, listener.envKey)
		}
	}
	if value == "" && listener.grpc {
		if grpcPort, ok := servicedefs.DefaultGRPCPort(serviceName); ok {
			return grpcPort, nil
		}
	}
	if listener.bindAddr && value != "" {
		_, portText, err := net.SplitHostPort(value)
		if err != nil {
			return 0, fmt.Errorf("%s: %s=%q is not host:port: %w", serviceName, listener.envKey, value, err)
		}
		value = portText
	}
	listenerPort, err := strconv.Atoi(value)
	if err != nil || listenerPort <= 0 || listenerPort > 65535 {
		return 0, fmt.Errorf("%s: %s=%q is not a port", serviceName, listener.envKey, value)
	}
	return listenerPort, nil
}

// composeInternalPKI is the internal-CA material a docker-mode service reads,
// from the paths in its env: the directories holding them, mounted read-only
// at the same paths, and the material the CLI bootstraps there. Privateer
// replaces ca.crt and the leaf by renaming over them, so the mounts are
// directories; a file mount would keep the replaced inode. The leaf key is
// readable by the frameworks group only, which the container joins. A service
// without internal TLS paths gets an empty pki.
func composeInternalPKI(env map[string]string, metadata map[string]any) (volumes []string, pki map[string]any) {
	caPath := strings.TrimSpace(env["GRPC_TLS_CA_PATH"])
	certPath := strings.TrimSpace(env["GRPC_TLS_CERT_PATH"])
	keyPath := strings.TrimSpace(env["GRPC_TLS_KEY_PATH"])
	if caPath == "" && certPath == "" && keyPath == "" {
		return nil, map[string]any{}
	}
	var dirs []string
	for _, path := range []string{caPath, certPath, keyPath} {
		if path != "" {
			dirs = append(dirs, filepath.Dir(path))
		}
	}
	sort.Strings(dirs)
	var mounted []string
	for _, dir := range dirs {
		covered := false
		for _, parent := range mounted {
			if dir == parent || strings.HasPrefix(dir, strings.TrimSuffix(parent, "/")+"/") {
				covered = true
				break
			}
		}
		if !covered {
			mounted = append(mounted, dir)
			volumes = append(volumes, dir+":"+dir+":ro")
		}
	}
	pki = map[string]any{
		"ca_path":       caPath,
		"cert_path":     certPath,
		"key_path":      keyPath,
		"ca_bundle_pem": metaString(metadata, "internal_ca_bundle_pem"),
		"cert_pem":      metaString(metadata, "internal_tls_cert_pem"),
		"key_pem":       metaString(metadata, "internal_tls_key_pem"),
	}
	return volumes, pki
}
