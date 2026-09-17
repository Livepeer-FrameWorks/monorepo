package handlers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/triggers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/golang-jwt/jwt/v5"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

func TestPreparedViewerHTTPQueryCredentialHandoff(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	policy := &commodorepb.ResolvePlaybackPolicyResponse{Type: "jwt", TenantId: "owner", JwtPolicy: &commodorepb.PlaybackJwtPolicy{
		ActiveKeys: []*commodorepb.PlaybackSigningKey{{Kid: "viewer", PublicKeyPem: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}},
	}}
	claims := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"sub": "viewer", "exp": time.Now().Add(time.Minute).Unix()})
	claims.Header["kid"] = "viewer"
	token, err := claims.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"public/hls/index.m3u8", "public/all"} {
		t.Run(path, func(t *testing.T) {
			setupPreparedViewerHTTP(t, false, &commodoreBalancingFake{
				playbackID: func(context.Context, *commodorepb.ResolvePlaybackIDRequest) (*commodorepb.ResolvePlaybackIDResponse, error) {
					return &commodorepb.ResolvePlaybackIDResponse{InternalName: "live+internal", TenantId: "owner", StreamId: "stream", RequiresAuth: true}, nil
				},
				playbackPolicy: func(context.Context, *commodorepb.ResolvePlaybackPolicyRequest) (*commodorepb.ResolvePlaybackPolicyResponse, error) {
					return policy, nil
				},
			})
			calls := 0
			logs := logrustest.NewLocal(logger)
			SetViewerPlacementPreparer(viewerPlacementFunc(func(_ context.Context, request control.ViewerPlacementRequest) (balancer.PlacementPreparationResult, error) {
				calls++
				endpoint := "https://us.example/hls/public/index.m3u8?receipt=prepared"
				if request.Protocol == "webrtc" {
					endpoint = "wss://us.example/webrtc/public?receipt=prepared"
				}
				return preparedHTTPViewer(t, request, endpoint), nil
			}))
			c, w := playbackCtxArms(t, path)
			c.Request.URL.RawQuery = url.Values{"jwt": {token}, "unrelated": {"do-not-forward"}}.Encode()
			HandleGenericViewerPlayback(c)
			for _, entry := range logs.AllEntries() {
				if strings.Contains(fmt.Sprint(entry.Data), token) || strings.Contains(entry.Message, token) {
					t.Fatal("viewer credential leaked into routing logs")
				}
			}
			if calls != 1 {
				t.Fatalf("preparation calls=%d status=%d body=%s", calls, w.Code, w.Body.String())
			}
			location := w.Header().Get("Location")
			if path == "public/all" {
				var body struct {
					Primary struct {
						URL     string `json:"url"`
						Outputs map[string]struct {
							URL string `json:"url"`
						} `json:"outputs"`
					} `json:"primary"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				location = body.Primary.URL
				for _, output := range body.Primary.Outputs {
					outputURL, parseErr := url.Parse(output.URL)
					if parseErr != nil || outputURL.Query().Get("jwt") != token {
						t.Fatal("full output catalog lost viewer credentials")
					}
				}
			} else if w.Code != http.StatusTemporaryRedirect {
				t.Fatalf("status=%d", w.Code)
			}
			u, err := url.Parse(location)
			if err != nil {
				t.Fatal(err)
			}
			if u.Host != "us.example" || u.Query().Get("jwt") != token || u.Query().Get("receipt") != "prepared" || u.Query().Has("unrelated") {
				t.Fatal("selected destination or credential handoff changed")
			}
			if w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatal("credential-bearing response can be cached or referred")
			}
			payload := strings.Join([]string{"live+internal", "203.0.113.10", u.Query().Get("jwt"), "HLS", location, "session"}, "\n")
			trigger, err := mist.ParseTriggerToProtobuf(mist.TriggerUserNew, []byte(payload), "us-edge", logger)
			if err != nil {
				t.Fatal(err)
			}
			if got := triggers.EvaluatePlaybackPolicy(context.Background(), logger, "internal", trigger.GetViewerConnect(), policy); got != "true" {
				t.Fatal("handoff token fails edge playback policy")
			}
			trigger.GetViewerConnect().ViewerToken = ""
			if got := triggers.EvaluatePlaybackPolicy(context.Background(), logger, "internal", trigger.GetViewerConnect(), policy); got != "false" {
				t.Fatal("edge policy accepted missing handoff token")
			}
		})
	}
}

func TestViewerQueryCredentialDoesNotMutateSharedEndpoint(t *testing.T) {
	original := &sharedpb.ViewerEndpointResponse{
		Primary: &sharedpb.ViewerEndpoint{Url: "https://us.example/live?receipt=kept", BaseUrl: "https://us.example",
			Outputs: map[string]*sharedpb.OutputEndpoint{"hls": {Url: "https://us.example/hls?receipt=kept"}, "absent": nil}},
		Fallbacks: []*sharedpb.ViewerEndpoint{nil, {Url: "https://other.example/live?jwt=old", BaseUrl: "https://other.example",
			Outputs: map[string]*sharedpb.OutputEndpoint{"hls": {Url: "https://other.example/hls?fwcid=kept"}}}},
	}
	first, err := withViewerQueryCredential(original, "first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := withViewerQueryCredential(original, "second")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(original.Primary.Url, "jwt") || first.Primary.Url == second.Primary.Url || first.Primary.BaseUrl != original.Primary.BaseUrl {
		t.Fatal("viewer credential leaked into shared catalog")
	}
	for _, endpoint := range []*sharedpb.ViewerEndpoint{first.Primary, first.Fallbacks[1]} {
		urls := []string{endpoint.Url}
		for _, output := range endpoint.Outputs {
			if output != nil && output.Url != "" {
				urls = append(urls, output.Url)
			}
		}
		for _, raw := range urls {
			u, err := url.Parse(raw)
			if err != nil || u.Query().Get("jwt") != "first" || len(u.Query()["jwt"]) != 1 {
				t.Fatal("catalog destination has missing or conflicting credentials")
			}
		}
	}
	if original.Fallbacks[1].Url != "https://other.example/live?jwt=old" || original.Primary.Outputs["hls"].Url != "https://us.example/hls?receipt=kept" {
		t.Fatal("nested shared catalog was mutated")
	}
}

func TestViewerQueryCredentialRejectsMalformedDestinations(t *testing.T) {
	for _, raw := range []string{"/relative", "https://user:password@us.example/live", "https://us.example/live#fragment", "https://us.example/live?receipt=%zz", "javascript://us.example/live"} {
		t.Run(raw, func(t *testing.T) {
			response := &sharedpb.ViewerEndpointResponse{Primary: &sharedpb.ViewerEndpoint{Url: raw}}
			if got, err := withViewerQueryCredential(response, "secret"); err == nil || got != nil {
				t.Fatal("malformed destination accepted")
			}
			if response.Primary.Url != raw {
				t.Fatal("malformed response mutated")
			}
		})
	}
}

func TestPreparedViewerHTTPDoesNotConvertHeaderCredentialToQuery(t *testing.T) {
	setupPreparedViewerHTTP(t, false)
	SetViewerPlacementPreparer(viewerPlacementFunc(func(_ context.Context, request control.ViewerPlacementRequest) (balancer.PlacementPreparationResult, error) {
		return preparedHTTPViewer(t, request, "https://us.example/hls/public/index.m3u8"), nil
	}))
	c, w := playbackCtxArms(t, "public/hls")
	c.Request.Header.Set("X-Frameworks-Playback-JWT", "header-secret")
	HandleGenericViewerPlayback(c)
	if w.Code != http.StatusTemporaryRedirect || strings.Contains(w.Header().Get("Location"), "header-secret") || strings.Contains(w.Body.String(), "header-secret") {
		t.Fatal("header credential was put into a URL")
	}
}
