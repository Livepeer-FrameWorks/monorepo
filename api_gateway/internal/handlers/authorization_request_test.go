package handlers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	authutil "github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/gin-gonic/gin"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type authorizationRequestClient struct {
	commodore.Interface
	calls            int
	info             ctxkeys.AuthClientMetadata
	user, tenant, ip string
	request          *commodorepb.CompleteAuthorizationRequest
}

func (c *authorizationRequestClient) CompleteAuthorization(ctx context.Context, request *commodorepb.CompleteAuthorizationRequest) (*commodorepb.CompleteAuthorizationResponse, error) {
	c.calls++
	c.info = ctxkeys.GetAuthClientMetadata(ctx)
	c.user = ctxkeys.GetUserID(ctx)
	c.tenant = ctxkeys.GetTenantID(ctx)
	c.ip = ctxkeys.GetClientIP(ctx)
	c.request = request
	return &commodorepb.CompleteAuthorizationResponse{Code: "one-use-code", ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))}, nil
}
func authorizationRequestInput() map[string]any {
	digest := sha256.Sum256([]byte("native-request-verifier-with-sufficient-entropy"))
	return map[string]any{"client_id": "tray-mac", "redirect_uri": "http://127.0.0.1:54321/callback", "code_challenge": base64.RawURLEncoding.EncodeToString(digest[:]), "code_challenge_method": "S256", "scope": "account", "state": "native-state"}
}
func authorizationTestRouter(key []byte, native ctxkeys.AuthClientMetadata, client *authorizationRequestClient) *gin.Engine {
	gin.SetMode(gin.TestMode)
	h := NewAuthHandlers(client, logging.NewLogger(), AuthConfig{Runtime: func() AuthRuntime { return AuthRuntime{JWTSecret: key} }})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		if c.Request.URL.Path == "/start" {
			ctx = ctxkeys.WithAuthClientMetadata(ctx, native)
		} else {
			ctx = ctxkeys.WithAuthClientMetadata(ctx, ctxkeys.AuthClientMetadata{IP: "198.51.100.22", DeviceLabel: "Browser override", Version: "browser-version", UserAgent: "browser-agent"})
			ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "browser-user")
			ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "browser-tenant")
			ctx = context.WithValue(ctx, ctxkeys.KeyClientIP, "198.51.100.22")
		}
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	router.POST("/start", h.AuthorizeStart())
	router.POST("/lookup", h.AuthorizeLookup())
	router.POST("/complete", h.AuthorizeComplete())
	return router
}
func authorizationPost(t *testing.T, router *gin.Engine, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, req)
	return response
}
func TestAuthorizationContextPreservesNativeProvenanceAndBrowserIdentity(t *testing.T) {
	for _, ip := range []string{"203.0.113.14", ""} {
		t.Run("source_"+ip, func(t *testing.T) {
			client := &authorizationRequestClient{}
			original := ctxkeys.AuthClientMetadata{IP: ip, DeviceLabel: "Original workstation", Version: "tray macOS arm64", UserAgent: "original-tray"}
			router := authorizationTestRouter([]byte("request-test-key"), original, client)
			input := authorizationRequestInput()
			started := authorizationPost(t, router, "/start", input)
			if started.Code != http.StatusOK || started.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("start=%d %s", started.Code, started.Body)
			}
			var result struct {
				RequestContext string `json:"request_context"`
			}
			if err := json.Unmarshal(started.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			preview := authorizationPost(t, router, "/lookup", map[string]any{"request_context": result.RequestContext, "device_label": "forged", "request_ip": "192.0.2.99"})
			if preview.Code != http.StatusOK || preview.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("lookup=%d %s", preview.Code, preview.Body)
			}
			var details map[string]any
			if err := json.Unmarshal(preview.Body.Bytes(), &details); err != nil {
				t.Fatal(err)
			}
			if details["device_label"] != original.DeviceLabel || details["request_ip"] != ip || details["client_version"] != original.Version || details["user_agent"] != original.UserAgent || details["requested_at"] == nil || details["expires_at"] == nil {
				t.Fatalf("native provenance replaced: %+v", details)
			}
			input["request_context"] = result.RequestContext
			input["user_id"] = "forged-user"
			input["tenant_id"] = "forged-tenant"
			completed := authorizationPost(t, router, "/complete", input)
			if completed.Code != http.StatusOK {
				t.Fatalf("complete=%d %s", completed.Code, completed.Body)
			}
			if client.calls != 1 || client.info != original || client.user != "browser-user" || client.tenant != "browser-tenant" || client.ip != ip || client.request.GetUserId() != "" || client.request.GetTenantId() != "" {
				t.Fatalf("identity or source changed: %+v", client)
			}
		})
	}
}
func TestAuthorizationContextRejectsChangedBindingBeforeIssuance(t *testing.T) {
	key := []byte("request-test-key")
	client := &authorizationRequestClient{}
	router := authorizationTestRouter(key, ctxkeys.AuthClientMetadata{DeviceLabel: "Original workstation"}, client)
	started := authorizationPost(t, router, "/start", authorizationRequestInput())
	var result struct {
		RequestContext string `json:"request_context"`
	}
	if err := json.Unmarshal(started.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"client_id", "redirect_uri", "code_challenge", "code_challenge_method", "scope", "state"} {
		t.Run(field, func(t *testing.T) {
			input := authorizationRequestInput()
			input["request_context"] = result.RequestContext
			input[field] = "changed"
			response := authorizationPost(t, router, "/complete", input)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("changed %s accepted: %d %s", field, response.Code, response.Body)
			}
		})
	}
	request := authutil.NativeAuthorizationRequest{ClientID: "tray-mac", RedirectURI: "http://127.0.0.1:54321/callback", CodeChallenge: authorizationRequestInput()["code_challenge"].(string), CodeChallengeMethod: "S256", Scope: "account", State: "native-state"}
	expired, err := authutil.SignNativeAuthorizationContext(request, key, time.Now().Add(-authutil.NativeAuthorizationContextTTL))
	if err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{expired, result.RequestContext + "changed"} {
		response := authorizationPost(t, router, "/lookup", map[string]string{"request_context": token})
		if response.Code != http.StatusBadRequest {
			t.Fatalf("bad context lookup=%d", response.Code)
		}
		input := authorizationRequestInput()
		input["request_context"] = token
		response = authorizationPost(t, router, "/complete", input)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("bad context complete=%d", response.Code)
		}
	}
	if client.calls != 0 {
		t.Fatalf("invalid request issued %d grants", client.calls)
	}
}
func TestAuthorizationContextUnavailableAndLegacyCompatibility(t *testing.T) {
	client := &authorizationRequestClient{}
	router := authorizationTestRouter(nil, ctxkeys.AuthClientMetadata{}, client)
	if response := authorizationPost(t, router, "/start", authorizationRequestInput()); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing key start=%d", response.Code)
	}
	if response := authorizationPost(t, router, "/lookup", map[string]string{"request_context": "present"}); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing key lookup=%d", response.Code)
	}
	input := authorizationRequestInput()
	input["request_context"] = "present"
	if response := authorizationPost(t, router, "/complete", input); response.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing key complete=%d", response.Code)
	}
	delete(input, "request_context")
	if response := authorizationPost(t, router, "/complete", input); response.Code != http.StatusOK || client.calls != 1 {
		t.Fatalf("legacy complete=%d calls=%d", response.Code, client.calls)
	}
}
