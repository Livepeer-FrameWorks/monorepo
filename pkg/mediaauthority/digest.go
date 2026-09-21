package mediaauthority

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash"
	"sort"

	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

const contentDigestInfo = "frameworks/media-authority/content-digest/v1"

// SecretCommitment binds a sealed secret's plaintext and recipient set without
// revealing either. Sealing draws a fresh ephemeral key and nonce per call, so
// the sealed boxes differ between two compiles of identical state; the
// commitment does not. The MAC key is derived from the signing key, so rotating
// the signer changes every commitment and forces re-issue.
func SecretCommitment(signingKey ed25519.PrivateKey, authorityID string, plaintext []byte, recipients []string) ([]byte, error) {
	if len(signingKey) != ed25519.PrivateKeySize || authorityID == "" {
		return nil, errors.New("secret commitment requires a signing key and authority ID")
	}
	key, err := hkdf.Key(sha256.New, signingKey.Seed(), []byte(authorityID), contentDigestInfo, sha256.Size)
	if err != nil {
		return nil, err
	}
	sorted := append([]string(nil), recipients...)
	sort.Strings(sorted)
	mac := hmac.New(sha256.New, key)
	writeDigestField(mac, plaintext)
	for _, recipient := range sorted {
		writeDigestField(mac, []byte(recipient))
	}
	return mac.Sum(nil), nil
}

// TenantContentDigest identifies a tenant authority's content. Tenant payloads
// carry no per-compile entropy, so the deterministic encoding is the content.
func TenantContentDigest(payload *mediaauthoritypb.TenantAuthority, signerKeyID string) ([]byte, error) {
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.New()
	writeDigestField(sum, encoded)
	writeDigestField(sum, []byte(signerKeyID))
	return sum.Sum(nil), nil
}

// TenantDependentsDigest covers the tenant fields media-object authorities are
// derived from. Allowances, limits, and decision text move with metering and
// never change what an object authority says, so they are excluded; everything
// else is kept, which errs toward fanning out.
func TenantDependentsDigest(payload *mediaauthoritypb.TenantAuthority) ([]byte, error) {
	dependents := proto.CloneOf(payload)
	dependents.Allowances = nil
	dependents.ResourceLimits = nil
	dependents.DecisionReason = ""
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(dependents)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(encoded)
	return sum[:], nil
}

// MediaObjectContentDigest identifies a media-object authority's content with
// sealed boxes replaced by their commitments. ok is false when the payload has
// no stable identity: it carries sealed boxes without a commitment for each
// sealed field, or commercial quotes, whose observation and expiry times are
// part of what the authority says.
func MediaObjectContentDigest(payload *mediaauthoritypb.MediaObjectAuthority, signerKeyID string, commitments [][]byte) (digest []byte, ok bool, err error) {
	if len(payload.GetCommercialQuotes()) > 0 {
		return nil, false, nil
	}
	// Commitments are ordered playback secret first, then live-stream secret.
	// The presence flags keep one committed field from standing in for the other.
	sealedFields := 0
	presence := []byte{0, 0}
	if len(payload.GetSealedPlaybackSecrets()) > 0 {
		sealedFields++
		presence[0] = 1
	}
	if len(payload.GetLiveStream().GetSealedCellSecrets()) > 0 {
		sealedFields++
		presence[1] = 1
	}
	if sealedFields != len(commitments) {
		return nil, false, nil
	}
	stable := proto.CloneOf(payload)
	stable.SealedPlaybackSecrets = nil
	if live := stable.GetLiveStream(); live != nil {
		live.SealedCellSecrets = nil
	}
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(stable)
	if err != nil {
		return nil, false, err
	}
	sum := sha256.New()
	writeDigestField(sum, encoded)
	writeDigestField(sum, []byte(signerKeyID))
	writeDigestField(sum, presence)
	for _, commitment := range commitments {
		writeDigestField(sum, commitment)
	}
	return sum.Sum(nil), true, nil
}

// writeDigestField length-prefixes each field so adjacent fields cannot be
// re-split into a different sequence with the same concatenation.
func writeDigestField(sum hash.Hash, field []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(field)))
	sum.Write(length[:])
	sum.Write(field)
}
