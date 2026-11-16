package handlers

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/triggers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/golang-jwt/jwt/v5"
	logrustest "github.com/sirupsen/logrus/hooks/test"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestVODViewerHTTPCredentialHandoff(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	policy := &commodorepb.ResolvePlaybackPolicyResponse{Type: "jwt", TenantId: "tenant-vod", JwtPolicy: &commodorepb.PlaybackJwtPolicy{
		ActiveKeys: []*commodorepb.PlaybackSigningKey{{Kid: "vod-viewer", PublicKeyPem: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}},
	}}
	claims := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"sub": "vod-viewer", "exp": time.Now().Add(time.Hour).Unix()})
	claims.Header["kid"] = "vod-viewer"
	token, err := claims.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, transport string
		status                int
	}{
		{"query redirect", "vod-public/hls/index.m3u8", "query", http.StatusTemporaryRedirect},
		{"query JSON", "vod-public", "query", http.StatusOK},
		{"missing", "vod-public/hls/index.m3u8", "", http.StatusForbidden},
		{"invalid", "vod-public/hls/index.m3u8", "invalid", http.StatusForbidden},
		{"header", "vod-public/hls/index.m3u8", "header", http.StatusTemporaryRedirect},
		{"cookie", "vod-public/hls/index.m3u8", "cookie", http.StatusTemporaryRedirect},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupPreparedViewerHTTP(t, false, &commodoreBalancingFake{
				playbackPolicy: func(context.Context, *commodorepb.ResolvePlaybackPolicyRequest) (*commodorepb.ResolvePlaybackPolicyResponse, error) {
					return policy, nil
				},
			})
			sm := balancingTestEnv(t)
			mock := withMockDBArms(t)
			const hash = "vodhashaaaaaaaaaaaaaaaaaaaaaaaa1"
			seedArtifactEdgeArms(t, sm, "vod-edge", "vod.example", "https://vod.example/", hash,
				map[string]any{"HLS": "https://vod.example/view/hls/$/index.m3u8"})
			startCommodoreFakeArms(t, &commodoreArmsFake{
				artifactPlaybackID: func(context.Context, *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
					return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: true, ArtifactHash: hash, InternalName: "art", TenantId: "tenant-vod", ContentType: "vod", RequiresAuth: true}, nil
				},
				vodHash: func(context.Context, *commodorepb.ResolveVodHashRequest) (*commodorepb.ResolveVodHashResponse, error) {
					return &commodorepb.ResolveVodHashResponse{Found: true, TenantId: "tenant-vod", Title: "Protected VOD"}, nil
				},
			})
			if tc.status != http.StatusForbidden {
				expectArtifactRowArms(mock, hash)
			}
			c, w := playbackCtxArms(t, tc.path)
			switch tc.transport {
			case "query":
				c.Request.URL.RawQuery = url.Values{"jwt": {token}, "unrelated": {"discard"}}.Encode()
			case "invalid":
				c.Request.URL.RawQuery = "jwt=invalid-viewer-token"
			case "header":
				c.Request.Header.Set("X-Frameworks-Playback-JWT", token)
			case "cookie":
				c.Request.AddCookie(&http.Cookie{Name: "jwt", Value: token})
			}
			logs := logrustest.NewLocal(logger)
			HandleGenericViewerPlayback(c)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s, want %d", w.Code, w.Body.String(), tc.status)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			if tc.status == http.StatusForbidden {
				if w.Header().Get("Location") != "" || !strings.Contains(w.Body.String(), "PLAYBACK_ACCESS_DENIED") {
					t.Fatal("denied VOD request disclosed a playback destination")
				}
				return
			}
			locations := []string{w.Header().Get("Location")}
			if tc.status == http.StatusOK {
				var response sharedpb.ViewerEndpointResponse
				if err := protojson.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.GetPrimary().GetNodeId() != "vod-edge" || len(response.GetPrimary().GetOutputs()) == 0 {
					t.Fatal("VOD JSON omitted the selected storage edge or output catalog")
				}
				locations = []string{response.Primary.Url}
				for _, output := range response.Primary.Outputs {
					locations = append(locations, output.Url)
				}
			}
			for _, location := range locations {
				u, err := url.Parse(location)
				if err != nil {
					t.Fatal(err)
				}
				session := mist.ViewerSessionID(location)
				if u.Host != "vod.example" || session == "" || u.Query().Has("jwt") || u.Query().Has("unrelated") {
					t.Fatal("VOD destination or distinct playback session was lost")
				}
				if tc.transport != "query" {
					if u.Query().Has(mist.ViewerJWTParam) || strings.Contains(location, token) {
						t.Fatal("non-query VOD credential leaked into a URL")
					}
					continue
				}
				if u.Query().Get(mist.ViewerJWTParam) != token {
					t.Fatal("VOD edge URL lost the query playback JWT")
				}
				viewer := &ipcpb.ViewerConnectTrigger{StreamName: "vod+art", ViewerToken: session, RequestUrl: location, Connector: "HLS"}
				if triggers.EvaluatePlaybackPolicy(t.Context(), logger, "art", viewer, policy) != "true" {
					t.Fatal("VOD edge policy rejected the forwarded credential")
				}
				u.RawQuery = url.Values{"tkn": {session}}.Encode()
				viewer.RequestUrl = u.String()
				if triggers.EvaluatePlaybackPolicy(t.Context(), logger, "art", viewer, policy) != "false" {
					t.Fatal("VOD edge policy accepted a session without the playback credential")
				}
			}
			for _, entry := range logs.AllEntries() {
				if strings.Contains(fmt.Sprint(entry.Data), token) || strings.Contains(entry.Message, token) {
					t.Fatal("VOD viewer credential leaked into routing logs")
				}
			}
			if w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
				t.Fatal("VOD credential-bearing response can be cached or referred")
			}
		})
	}
}
