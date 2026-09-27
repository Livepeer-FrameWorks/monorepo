package grpcutil

import (
	"slices"
	"strings"
	"time"

	"google.golang.org/grpc"
	// Registers the client-side health checking function that the
	// healthCheckConfig in ReplicaServiceConfig needs; without it gRPC ignores
	// the setting and keeps routing to a draining replica.
	_ "google.golang.org/grpc/health"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/resolver/manual"
)

// ReplicaServiceConfig spreads RPCs over every address a target resolves to
// and keeps an address out of the pick set while its standard health service
// reports anything other than SERVING. A replica that shuts down flips its
// health to NOT_SERVING (pkg/server.DrainGRPCHealth) before its listener
// closes, so callers move to the other replicas before any RPC fails. A server
// without the health service is treated as healthy.
const ReplicaServiceConfig = `{"loadBalancingConfig":[{"round_robin":{}}],"healthCheckConfig":{"serviceName":""}}`

// ReplicaClientKeepalive pings every connection that carries a stream, which
// includes the health Watch that ReplicaServiceConfig keeps open on each
// replica. A replica whose host dies without closing its sockets is then torn
// down within Time+Timeout instead of holding the RPCs balanced onto it until
// TCP gives up. Servers must accept it: see ReplicaServerKeepalive.
func ReplicaClientKeepalive() grpc.DialOption {
	return grpc.WithKeepaliveParams(keepalive.ClientParameters{Time: 30 * time.Second, Timeout: 10 * time.Second})
}

// ReplicaServerKeepalive admits ReplicaClientKeepalive's pings. gRPC's default
// policy (one ping per 5 minutes) answers faster pings with GOAWAY.
func ReplicaServerKeepalive() grpc.ServerOption {
	return grpc.KeepaliveEnforcementPolicy(keepalive.EnforcementPolicy{MinTime: 15 * time.Second})
}

// replicaListScheme names the per-connection resolver that serves an explicit
// address list. It is registered on one ClientConn only (grpc.WithResolvers).
const replicaListScheme = "fw-replicas"

// ParseReplicaAddrs splits a gRPC address setting into its entries. The value
// is one host:port or a comma-separated list of them, each one replica of the
// same service. Blank and repeated entries are dropped.
func ParseReplicaAddrs(raw string) []string {
	var addrs []string
	for entry := range strings.SplitSeq(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" || slices.Contains(addrs, entry) {
			continue
		}
		addrs = append(addrs, entry)
	}
	return addrs
}

// ReplicaTarget returns the dial target and options for a service that may
// run as several replicas. A single address keeps gRPC's default dns
// resolver, which yields every record behind the name (commodore.internal
// lists each running instance). A comma-separated list is served as a fixed
// address set, one subchannel per entry. Both use ReplicaServiceConfig and
// ReplicaClientKeepalive.
func ReplicaTarget(raw string) (string, []grpc.DialOption) {
	opts := []grpc.DialOption{grpc.WithDefaultServiceConfig(ReplicaServiceConfig), ReplicaClientKeepalive()}
	addrs := ParseReplicaAddrs(raw)
	if len(addrs) <= 1 {
		return strings.TrimSpace(raw), opts
	}
	r := manual.NewBuilderWithScheme(replicaListScheme)
	state := resolver.State{Addresses: make([]resolver.Address, 0, len(addrs))}
	for _, addr := range addrs {
		state.Addresses = append(state.Addresses, resolver.Address{Addr: addr})
	}
	r.InitialState(state)
	// The first entry is the target's authority. TLS without a configured
	// server name verifies every replica against that entry's host.
	return replicaListScheme + ":///" + addrs[0], append(opts, grpc.WithResolvers(r))
}
