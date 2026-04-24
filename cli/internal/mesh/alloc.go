// Package mesh re-exports the shared WireGuard keypair / allocator helpers
// from pkg/mesh so `frameworks mesh wg generate` call sites stay stable. The
// primitives themselves live in pkg/mesh so Quartermaster can reuse them
// (same deterministic IP allocation, same keypair semantics).
package mesh

import (
	"net"

	pkgmesh "frameworks/pkg/mesh"
)

// GenerateKeyPair is an alias for pkg/mesh.GenerateKeyPair.
func GenerateKeyPair() (private, public string, err error) {
	return pkgmesh.GenerateKeyPair()
}

// DerivePublicKey is an alias for pkg/mesh.DerivePublicKey.
func DerivePublicKey(privateKey string) (string, error) {
	return pkgmesh.DerivePublicKey(privateKey)
}

// AllocateMeshIP is an alias for pkg/mesh.AllocateMeshIP.
func AllocateMeshIP(clusterName, hostName string, cidr *net.IPNet, taken map[string]struct{}) (net.IP, error) {
	return pkgmesh.AllocateMeshIP(clusterName, hostName, cidr, taken)
}
