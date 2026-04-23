// Package mesh holds WireGuard keypair and mesh-IP allocation helpers used
// by `frameworks mesh wg generate`. Allocation is deterministic per (cluster,
// host) so repeated runs on the same manifest are no-ops.
package mesh

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"

	"golang.org/x/crypto/curve25519"
)

// GenerateKeyPair produces a WireGuard-compatible curve25519 keypair. Returns
// (privateKey, publicKey) as base64-encoded 32-byte values.
func GenerateKeyPair() (private, public string, err error) {
	var priv [32]byte
	if _, randErr := rand.Read(priv[:]); randErr != nil {
		return "", "", fmt.Errorf("read random bytes: %w", randErr)
	}
	// WireGuard clamp — identical to what `wg genkey` produces.
	priv[0] &= 248
	priv[31] &= 127
	priv[31] |= 64

	pub, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return "", "", fmt.Errorf("derive public key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(priv[:]), base64.StdEncoding.EncodeToString(pub), nil
}

// DerivePublicKey returns the WireGuard public key for a base64-encoded
// 32-byte private key.
func DerivePublicKey(privateKey string) (string, error) {
	priv, err := base64.StdEncoding.DecodeString(privateKey)
	if err != nil {
		return "", fmt.Errorf("decode private key: %w", err)
	}
	if len(priv) != 32 {
		return "", fmt.Errorf("private key must decode to 32 bytes, got %d", len(priv))
	}
	pub, err := curve25519.X25519(priv, curve25519.Basepoint)
	if err != nil {
		return "", fmt.Errorf("derive public key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(pub), nil
}

// AllocateMeshIP returns a deterministic /32 mesh address for the given
// (clusterName, hostName) inside cidr, skipping .0 and .1 and any IPs
// already present in taken. Determinism keeps re-runs idempotent.
func AllocateMeshIP(clusterName, hostName string, cidr *net.IPNet, taken map[string]struct{}) (net.IP, error) {
	ones, bits := cidr.Mask.Size()
	if bits != 32 {
		return nil, fmt.Errorf("mesh CIDR must be IPv4, got /%d bits=%d", ones, bits)
	}
	hostBits := uint32(bits - ones)
	if hostBits < 4 {
		return nil, fmt.Errorf("mesh CIDR %s too small (need at least /28)", cidr)
	}
	hostMax := uint32(1) << hostBits
	base := binary.BigEndian.Uint32(cidr.IP.To4())

	seed := sha256.Sum256([]byte(clusterName + "\x00" + hostName))
	offset := binary.BigEndian.Uint32(seed[:4]) % hostMax

	for i := range hostMax {
		candidate := (offset + i) % hostMax
		if candidate < 2 || candidate == hostMax-1 {
			// Reserve .0 (network), .1 (gateway convention), broadcast.
			continue
		}
		ip := make(net.IP, 4)
		binary.BigEndian.PutUint32(ip, base+candidate)
		if _, clash := taken[ip.String()]; clash {
			continue
		}
		return ip, nil
	}
	return nil, fmt.Errorf("mesh CIDR %s exhausted", cidr)
}
