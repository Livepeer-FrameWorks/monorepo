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
		chapter, matroska     bool
	}{
		{name: "query redirect", path: "vod-public/hls/index.m3u8", transport: "query", status: http.StatusTemporaryRedirect},
		{name: "query JSON", path: "vod-public", transport: "query", status: http.StatusOK},
		{name: "missing", path: "vod-public/hls/index.m3u8", status: http.StatusForbidden},
		{name: "invalid", path: "vod-public/hls/index.m3u8", transport: "invalid", status: http.StatusForbidden},
		{name: "header", path: "vod-public/hls/index.m3u8", transport: "header", status: http.StatusTemporaryRedirect},
		{name: "cookie", path: "vod-public/hls/index.m3u8", transport: "cookie", status: http.StatusTemporaryRedirect},
		{name: "chapter Matroska", path: "chapter-public.mkv", transport: "query", status: http.StatusTemporaryRedirect, chapter: true, matroska: true},
		{name: "chapter without Matroska", path: "chapter-public.mkv", transport: "query", status: http.StatusNotFound, chapter: true},
		{name: "chapter Matroska denied", path: "chapter-public.mkv", transport: "invalid", status: http.StatusForbidden, chapter: true, matroska: true},
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
			contentType := "vod"
			outputs := map[string]any{"HLS": "https://vod.example/view/hls/$/index.m3u8"}
			if tc.chapter {
				contentType = "chapter"
				outputs = matroskaPlaybackOutputs(t, "https://vod.example/view", tc.matroska)
			}
			seedArtifactEdgeArms(t, sm, "vod-edge", "vod.example", "https://vod.example/", hash, outputs)
			startCommodoreFakeArms(t, &commodoreArmsFake{
				artifactPlaybackID: func(_ context.Context, req *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
					if tc.chapter && req.GetPlaybackId() != "chapter-public" {
						t.Errorf("chapter resolver received %q instead of the public chapter ID", req.GetPlaybackId())
					}
					return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: true, ArtifactHash: hash, InternalName: "art", TenantId: "tenant-vod", ContentType: contentType, RequiresAuth: true}, nil
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
				query := url.Values{"jwt": {token}, "unrelated": {"discard"}}
				if tc.chapter {
					query.Set("start", "2")
					query.Set("stop", "6")
				}
				c.Request.URL.RawQuery = query.Encode()
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
			if tc.status == http.StatusNotFound {
				if w.Header().Get("Location") != "" || !strings.Contains(w.Body.String(), "PROTOCOL_NOT_AVAILABLE") {
					t.Fatal("unavailable native output did not return a protocol-specific 404")
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
				if u.Host != "vod.example" || session == "" || session == token || u.Query().Has("jwt") || u.Query().Has("unrelated") {
					t.Fatal("VOD destination or distinct playback session was lost")
				}
				if tc.chapter && (u.Path != "/view/chapter-public.mkv" || u.Query().Get("start") != "2" || u.Query().Get("stop") != "6") {
					t.Fatal("Matroska redirect lost the public chapter identity or requested interval")
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
				connector := "HLS"
				if tc.chapter {
					connector = "EBML"
				}
				viewer := &ipcpb.ViewerConnectTrigger{StreamName: "vod+art", ViewerToken: session, RequestUrl: location, Connector: connector}
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
