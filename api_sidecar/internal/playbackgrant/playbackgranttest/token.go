// Package playbackgranttest mints ES256 viewer tokens and grants for tests.
package playbackgranttest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// SigningKey is a tenant playback signing key.
type SigningKey struct {
	Kid     string
	Private *ecdsa.PrivateKey
	PEM     string
}

// NewSigningKey generates a P-256 key under kid.
func NewSigningKey(t *testing.T, kid string) SigningKey {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return SigningKey{Kid: kid, Private: priv, PEM: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}
}

// Token signs a viewer JWT expiring at exp.
func (k SigningKey) Token(t *testing.T, subject string, exp time.Time) string {
	t.Helper()
	enc := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	signingInput := enc(map[string]string{"alg": "ES256", "typ": "JWT", "kid": k.Kid}) + "." +
		enc(map[string]any{"sub": subject, "exp": exp.Unix(), "iat": time.Now().Unix()})
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, k.Private, digest[:])
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// Grant builds a grant for internal, reachable as names, valid for a day.
func Grant(internal string, kind ipcpb.PlaybackGrantPolicyKind, keys []SigningKey, names ...string) *ipcpb.PlaybackGrant {
	policy := &ipcpb.PlaybackGrantPolicy{Kind: kind}
	for _, key := range keys {
		policy.ActiveKeys = append(policy.ActiveKeys, &ipcpb.PlaybackGrantKey{Kid: key.Kid, PublicKeyPem: key.PEM})
	}
	return &ipcpb.PlaybackGrant{
		InternalName: internal, RequestedNames: names, TenantId: "tenant-a", Policy: policy,
		ObjectAuthorityVersion: 1, TenantAuthorityVersion: 1, ValidUntil: timestamppb.New(time.Now().Add(24 * time.Hour)),
	}
}
