package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/gin-gonic/gin"
)

// withFakeSessionValidator swaps the package-level validator for the duration
// of a test so the middleware can run without a real Foghorn control stream.
func withFakeSessionValidator(
	t *testing.T,
	session func(ctx context.Context, token string) (*pb.EdgeMistAdminSessionResponse, error),
) {
	t.Helper()
	prevSession := validateMistAdminSession
	t.Cleanup(func() {
		validateMistAdminSession = prevSession
	})
	if session != nil {
		validateMistAdminSession = session
	}
}

func TestRequireMistAdmin_AcceptsValidCookie(t *testing.T) {
	withFakeSessionValidator(t,
		func(_ context.Context, token string) (*pb.EdgeMistAdminSessionResponse, error) {
			if token != "cookie-token" {
				t.Errorf("unexpected token forwarded: %q", token)
			}
			return &pb.EdgeMistAdminSessionResponse{
				Valid:     true,
				UserId:    "u1",
				TenantId:  "t1",
				Role:      "owner",
				NodeId:    "edge-us-1",
				ExpiresAt: time.Now().Add(5 * time.Minute).Unix(),
			}, nil
		})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	hit := false
	r.GET("/_mist/probe", RequireMistAdmin(logging.NewLogger()), func(c *gin.Context) {
		hit = true
		if c.GetString("mist_admin_user_id") != "u1" {
			t.Errorf("context user_id missing")
		}
		c.Status(http.StatusOK)
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/_mist/probe", nil)
	req.AddCookie(&http.Cookie{Name: MistAdminCookieName, Value: "cookie-token"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("want 200; got %d", resp.StatusCode)
	}
	if !hit {
		t.Error("downstream handler should have been reached")
	}
}

func TestRequireMistAdmin_RejectsBearerWithoutSessionCookie(t *testing.T) {
	withFakeSessionValidator(t,
		func(_ context.Context, _ string) (*pb.EdgeMistAdminSessionResponse, error) {
			t.Fatalf("session validator should not be called without a cookie")
			return nil, nil
		})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/_mist/api2", RequireMistAdmin(logging.NewLogger()), func(c *gin.Context) {
		t.Fatal("downstream must not run for a bearer-only request")
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/_mist/api2", nil)
	req.Header.Set("Authorization", "Bearer dev-api-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401; got %d", resp.StatusCode)
	}
}

func TestRequireMistAdmin_RejectsInvalidCookie(t *testing.T) {
	withFakeSessionValidator(t,
		func(_ context.Context, _ string) (*pb.EdgeMistAdminSessionResponse, error) {
			return &pb.EdgeMistAdminSessionResponse{Valid: false}, nil
		})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/_mist/probe", RequireMistAdmin(logging.NewLogger()), func(c *gin.Context) {
		t.Fatal("downstream must not run with an invalid cookie and no bearer")
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/_mist/probe", nil)
	req.AddCookie(&http.Cookie{Name: MistAdminCookieName, Value: "stale"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401; got %d", resp.StatusCode)
	}
}

func TestRequireMistAdmin_ValidationUnavailable(t *testing.T) {
	withFakeSessionValidator(t,
		func(_ context.Context, _ string) (*pb.EdgeMistAdminSessionResponse, error) {
			return nil, errors.New("control stream disconnected")
		})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/_mist/probe", RequireMistAdmin(logging.NewLogger()), func(c *gin.Context) {
		t.Fatal("downstream must not run when validation is unavailable")
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/_mist/probe", nil)
	req.AddCookie(&http.Cookie{Name: MistAdminCookieName, Value: "fresh"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("want 503; got %d", resp.StatusCode)
	}
}

func TestRequireMistAdmin_RejectsExpiredCookie(t *testing.T) {
	withFakeSessionValidator(t,
		func(_ context.Context, _ string) (*pb.EdgeMistAdminSessionResponse, error) {
			return &pb.EdgeMistAdminSessionResponse{
				Valid:     true,
				ExpiresAt: time.Now().Add(-1 * time.Second).Unix(),
			}, nil
		})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/_mist/probe", RequireMistAdmin(logging.NewLogger()), func(c *gin.Context) {
		t.Fatal("downstream must not run with an expired cookie")
	})

	srv := httptest.NewServer(r)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/_mist/probe", nil)
	req.AddCookie(&http.Cookie{Name: MistAdminCookieName, Value: "expired"})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401; got %d", resp.StatusCode)
	}
}

func TestMistAdminSessionHandler_PostSetsCookieAndRedirects(t *testing.T) {
	exp := time.Now().Add(5 * time.Minute).Unix()
	withFakeSessionValidator(t,
		func(_ context.Context, token string) (*pb.EdgeMistAdminSessionResponse, error) {
			if token != "fresh-session-token" {
				t.Errorf("unexpected token: %q", token)
			}
			return &pb.EdgeMistAdminSessionResponse{
				Valid:     true,
				UserId:    "u1",
				TenantId:  "t1",
				Role:      "owner",
				NodeId:    "edge-us-1",
				ClusterId: "media-us-1",
				ExpiresAt: exp,
			}, nil
		})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/_mist-session", MistAdminSessionHandler(logging.NewLogger()))

	srv := httptest.NewServer(r)
	defer srv.Close()

	form := url.Values{}
	form.Set("session_token", "fresh-session-token")
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/_mist-session", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// Disable redirect-following so we can inspect the 302 directly.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Fatalf("want 302; got %d", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/_mist/" {
		t.Errorf("redirect target: got %q, want %q", loc, "/_mist/")
	}

	// Cookie attributes — the SECURITY-critical assertions.
	cookies := resp.Cookies()
	var c *http.Cookie
	for _, ck := range cookies {
		if ck.Name == MistAdminCookieName {
			c = ck
			break
		}
	}
	if c == nil {
		t.Fatalf("missing %s cookie in response", MistAdminCookieName)
	}
	if c.Value != "fresh-session-token" {
		t.Errorf("cookie value mismatch: %q", c.Value)
	}
	if !c.HttpOnly {
		t.Error("cookie must be HttpOnly to keep it out of LSP JS reach")
	}
	if !c.Secure {
		t.Error("cookie must be Secure")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Errorf("cookie SameSite: got %v, want Lax", c.SameSite)
	}
	if c.Path != MistAdminCookiePath {
		t.Errorf("cookie path: got %q, want %q (must NOT scope to /)", c.Path, MistAdminCookiePath)
	}
	if c.MaxAge <= 0 || c.MaxAge > int(5*time.Minute/time.Second) {
		t.Errorf("cookie MaxAge: got %d, want 0 < MaxAge <= 300", c.MaxAge)
	}
}

func TestMistAdminSessionHandler_RejectsGET(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Register on Any so the handler itself gets to enforce the method check.
	r.Any("/_mist-session", MistAdminSessionHandler(logging.NewLogger()))
	srv := httptest.NewServer(r)
	defer srv.Close()

	resp := getCtx(t, srv.URL+"/_mist-session")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /_mist-session: want 405; got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Allow"); got != http.MethodPost {
		t.Errorf("Allow header: got %q, want POST", got)
	}
}

func TestMistAdminSessionHandler_RejectsMissingToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/_mist-session", MistAdminSessionHandler(logging.NewLogger()))
	srv := httptest.NewServer(r)
	defer srv.Close()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/_mist-session", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("want 400; got %d", resp.StatusCode)
	}
}

func TestMistAdminSessionHandler_RejectsInvalidToken(t *testing.T) {
	withFakeSessionValidator(t,
		func(_ context.Context, _ string) (*pb.EdgeMistAdminSessionResponse, error) {
			return &pb.EdgeMistAdminSessionResponse{Valid: false}, nil
		})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/_mist-session", MistAdminSessionHandler(logging.NewLogger()))
	srv := httptest.NewServer(r)
	defer srv.Close()

	form := url.Values{}
	form.Set("session_token", "rejected")
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/_mist-session", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401; got %d", resp.StatusCode)
	}
	if len(resp.Cookies()) != 0 {
		t.Error("invalid token must not set any cookie")
	}
}

func TestMistAdminSessionHandler_ValidationUnavailable(t *testing.T) {
	withFakeSessionValidator(t,
		func(_ context.Context, _ string) (*pb.EdgeMistAdminSessionResponse, error) {
			return nil, errors.New("control stream disconnected")
		})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/_mist-session", MistAdminSessionHandler(logging.NewLogger()))
	srv := httptest.NewServer(r)
	defer srv.Close()

	form := url.Values{}
	form.Set("session_token", "fresh")
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/_mist-session", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("want 503; got %d", resp.StatusCode)
	}
	if len(resp.Cookies()) != 0 {
		t.Error("validation failure must not set any cookie")
	}
}

func TestMistAdminSessionHandler_RejectsExpiredToken(t *testing.T) {
	// Token that has technically validated but has zero TTL left — the
	// handler must refuse to set a cookie with a non-positive Max-Age.
	withFakeSessionValidator(t,
		func(_ context.Context, _ string) (*pb.EdgeMistAdminSessionResponse, error) {
			return &pb.EdgeMistAdminSessionResponse{
				Valid:     true,
				UserId:    "u1",
				TenantId:  "t1",
				NodeId:    "edge-us-1",
				ExpiresAt: time.Now().Add(-1 * time.Second).Unix(), // already past
			}, nil
		})
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/_mist-session", MistAdminSessionHandler(logging.NewLogger()))
	srv := httptest.NewServer(r)
	defer srv.Close()

	form := url.Values{}
	form.Set("session_token", "past")
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/_mist-session", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("want 401; got %d", resp.StatusCode)
	}
}
