package frameworks

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"
)

type jwtFixtureInput struct {
	Subject   string         `json:"subject"`
	Audience  []string       `json:"audience"`
	ExpiresAt int64          `json:"expiresAt"`
	IssuedAt  int64          `json:"issuedAt"`
	NotBefore int64          `json:"notBefore"`
	Claims    map[string]any `json:"claims"`
}

type jwtFixture struct {
	Key struct {
		Kid           string `json:"kid"`
		PrivateKeyPem string `json:"privateKeyPem"`
		PublicKeyPem  string `json:"publicKeyPem"`
	} `json:"key"`
	Cases []struct {
		Name         string          `json:"name"`
		Input        jwtFixtureInput `json:"input"`
		SigningInput string          `json:"signingInput"`
	} `json:"cases"`
	Rejected []struct {
		Name  string          `json:"name"`
		Input jwtFixtureInput `json:"input"`
	} `json:"rejected"`
	Tokens map[string]string `json:"tokens"`
}

func (in jwtFixtureInput) options(kid, pemKey string) PlaybackTokenOptions {
	unix := func(s int64) time.Time {
		if s == 0 {
			return time.Time{}
		}
		return time.Unix(s, 0)
	}
	return PlaybackTokenOptions{
		PrivateKeyPEM: pemKey,
		Kid:           kid,
		ExpiresAt:     unix(in.ExpiresAt),
		IssuedAt:      unix(in.IssuedAt),
		NotBefore:     unix(in.NotBefore),
		Subject:       in.Subject,
		Audience:      in.Audience,
		Claims:        in.Claims,
	}
}

func verifiesUnderKey(t *testing.T, token, publicPEM string) bool {
	t.Helper()
	block, _ := pem.Decode([]byte(publicPEM))
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return false
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(sig) != 64 {
		return false
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	return ecdsa.Verify(pub.(*ecdsa.PublicKey), digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:]))
}

func TestPlaybackTokenSigning(t *testing.T) {
	var fx jwtFixture
	loadFixture(t, "playback_jwt.json", &fx)
	for _, tc := range fx.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			token, err := SignPlaybackToken(tc.Input.options(fx.Key.Kid, fx.Key.PrivateKeyPem))
			if err != nil {
				t.Fatal(err)
			}
			parts := strings.Split(token, ".")
			if got := parts[0] + "." + parts[1]; got != tc.SigningInput {
				t.Fatalf("signing input\n got %s\nwant %s", got, tc.SigningInput)
			}
			if !verifiesUnderKey(t, token, fx.Key.PublicKeyPem) {
				t.Fatal("signature does not verify under the fixture public key")
			}
		})
	}
	for _, tc := range fx.Rejected {
		t.Run("rejects: "+tc.Name, func(t *testing.T) {
			if _, err := PlaybackTokenSigningInput(tc.Input.options(fx.Key.Kid, "")); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	t.Run("the Go token in the fixture was signed by this SDK over the claims case", func(t *testing.T) {
		token, ok := fx.Tokens["go"]
		if !ok {
			t.Skip("sdk_conformance/playback_jwt.json has no tokens.go yet")
		}
		parts := strings.Split(token, ".")
		if len(parts) != 3 || parts[0]+"."+parts[1] != fx.Cases[1].SigningInput {
			t.Fatalf("tokens.go does not carry the claims case's signing input")
		}
		if !verifiesUnderKey(t, token, fx.Key.PublicKeyPem) {
			t.Fatal("tokens.go does not verify under the fixture public key")
		}
	})
}
