package triggers

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// sdkJWTFixture is sdk_conformance/playback_jwt.json: the shared playback
// token vectors of the TypeScript, Go, and Python SDKs.
type sdkJWTFixture struct {
	Key struct {
		Kid          string `json:"kid"`
		PublicKeyPem string `json:"publicKeyPem"`
	} `json:"key"`
	Cases []struct {
		Name         string `json:"name"`
		SigningInput string `json:"signingInput"`
		Policy       struct {
			AllowedKids        []string          `json:"allowedKids"`
			RequiredAudience   []string          `json:"requiredAudience"`
			RequiredClaimsJSON map[string]string `json:"requiredClaimsJson"`
		} `json:"policy"`
	} `json:"cases"`
	Tokens map[string]string `json:"tokens"`
}

func loadSDKJWTFixture(t *testing.T) sdkJWTFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "sdk_conformance", "playback_jwt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f sdkJWTFixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// The playback tokens each SDK signs must pass the same policy evaluation
// USER_NEW runs, so a tenant can mint viewer tokens with any SDK.
func TestSDKPlaybackTokensPassThePlaybackVerifier(t *testing.T) {
	f := loadSDKJWTFixture(t)
	claimsCase := f.Cases[1]
	policy := &commodorepb.ResolvePlaybackPolicyResponse{
		Type: "jwt",
		JwtPolicy: &commodorepb.PlaybackJwtPolicy{
			AllowedKids:        claimsCase.Policy.AllowedKids,
			RequiredAudience:   claimsCase.Policy.RequiredAudience,
			RequiredClaimsJson: claimsCase.Policy.RequiredClaimsJSON,
			ActiveKeys:         []*commodorepb.PlaybackSigningKey{{Kid: f.Key.Kid, PublicKeyPem: f.Key.PublicKeyPem}},
		},
	}
	evaluate := func(token string, p *commodorepb.ResolvePlaybackPolicyResponse) *PlaybackDecision {
		return EvaluatePlaybackPolicyDetailed(context.Background(), testPlaybackAuthProcessor().logger, "live+fixture",
			&ipcpb.ViewerConnectTrigger{ViewerToken: token}, p, nil)
	}

	for _, lang := range []string{"typescript", "go", "python"} {
		t.Run(lang, func(t *testing.T) {
			token := f.Tokens[lang]
			if token == "" {
				t.Fatalf("sdk_conformance/playback_jwt.json has no %s token", lang)
			}
			parts := strings.Split(token, ".")
			if len(parts) != 3 || parts[0]+"."+parts[1] != claimsCase.SigningInput {
				t.Fatalf("%s token does not sign the %q case's signing input", lang, claimsCase.Name)
			}
			if d := evaluate(token, policy); !d.Allowed || d.Kid != f.Key.Kid {
				t.Fatalf("verifier denied the %s token: reason=%q detail=%q", lang, d.Reason, d.Detail)
			}

			// The same token must fail when the policy asks for something it
			// does not carry, so the allow above is the verifier's own decision.
			strict := &commodorepb.ResolvePlaybackPolicyResponse{Type: "jwt", JwtPolicy: &commodorepb.PlaybackJwtPolicy{
				AllowedKids:        policy.JwtPolicy.AllowedKids,
				RequiredAudience:   []string{"embed"},
				RequiredClaimsJson: policy.JwtPolicy.RequiredClaimsJson,
				ActiveKeys:         policy.JwtPolicy.ActiveKeys,
			}}
			if d := evaluate(token, strict); d.Allowed || d.Reason != "jwt-aud-mismatch" {
				t.Fatalf("audience embed: got allowed=%v reason=%q, want deny jwt-aud-mismatch", d.Allowed, d.Reason)
			}
			tampered := parts[0] + "." + parts[1] + "." + strings.Repeat("A", len(parts[2]))
			if d := evaluate(tampered, policy); d.Allowed {
				t.Fatal("a token with a zeroed signature was allowed")
			}
		})
	}
}
