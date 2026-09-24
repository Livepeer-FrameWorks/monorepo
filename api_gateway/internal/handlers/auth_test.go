package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"frameworks/api_gateway/internal/clients/clientstest"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"

	"github.com/gin-gonic/gin"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestAuthHandlers_InvalidJSONBindingsReturnBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := &AuthHandlers{
		logger: logging.NewLogger(),
	}

	tests := []struct {
		name    string
		handler gin.HandlerFunc
	}{
		{name: "login", handler: h.Login()},
		{name: "wallet login", handler: h.WalletLogin()},
		{name: "register", handler: h.Register()},
		{name: "verify email", handler: h.VerifyEmail()},
		{name: "resend verification", handler: h.ResendVerification()},
		{name: "forgot password", handler: h.ForgotPassword()},
		{name: "reset password", handler: h.ResetPassword()},
		{name: "update me", handler: h.UpdateMe()},
		{name: "update newsletter", handler: h.UpdateNewsletter()},
		{name: "authorize complete", handler: h.AuthorizeComplete()},
		{name: "oauth token", handler: h.OAuthToken()},
		{name: "device start", handler: h.DeviceStart()},
		{name: "device poll", handler: h.DevicePoll()},
		{name: "device lookup", handler: h.DeviceLookup()},
		{name: "device approve", handler: h.DeviceApprove()},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/test", strings.NewReader(`{"broken_json":`))
			c.Request.Header.Set("Content-Type", "application/json")

			tc.handler(c)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status: got %d, want %d", rec.Code, http.StatusBadRequest)
			}
			if !strings.Contains(rec.Body.String(), "invalid request") {
				t.Fatalf("body: expected invalid request error, got %q", rec.Body.String())
			}
		})
	}
}

func TestVerifyEmailAcceptsTokenOnlyInJSONBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var gotToken string
	fake := &clientstest.FakeCommodore{
		VerifyEmailFn: func(_ context.Context, token string) (*commodorepb.VerifyEmailResponse, error) {
			gotToken = token
			return &commodorepb.VerifyEmailResponse{Success: true, Message: "verified"}, nil
		},
	}
	h := &AuthHandlers{commodore: fake, logger: logging.NewLogger()}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/verify", strings.NewReader(`{"token":"secret-token"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	h.VerifyEmail()(c)
	if recorder.Code != http.StatusOK || gotToken != "secret-token" {
		t.Fatalf("status = %d, token = %q, body = %s", recorder.Code, gotToken, recorder.Body.String())
	}
}

func TestRecoveryHandlersForwardTurnstileTokens(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var resendToken, forgotToken string
	fake := &clientstest.FakeCommodore{
		ResendVerificationFn: func(_ context.Context, _, token string) (*commodorepb.ResendVerificationResponse, error) {
			resendToken = token
			return &commodorepb.ResendVerificationResponse{Success: true, Message: "accepted"}, nil
		},
		ForgotPasswordFn: func(_ context.Context, _, token string) (*commodorepb.ForgotPasswordResponse, error) {
			forgotToken = token
			return &commodorepb.ForgotPasswordResponse{Success: true, Message: "accepted"}, nil
		},
	}
	h := &AuthHandlers{commodore: fake, logger: logging.NewLogger()}
	for path, handler := range map[string]gin.HandlerFunc{
		"/auth/resend-verification": h.ResendVerification(),
		"/auth/forgot-password":     h.ForgotPassword(),
	} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(`{"email":"user@example.com","turnstile_token":"proof"}`))
		c.Request.Header.Set("Content-Type", "application/json")
		handler(c)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", path, recorder.Code, recorder.Body.String())
		}
	}
	if resendToken != "proof" || forgotToken != "proof" {
		t.Fatalf("turnstile tokens = resend %q forgot %q", resendToken, forgotToken)
	}
}

func TestRecoveryHandlersSurfaceBotCheckFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	botError := grpcutil.SanitizeError(grpcutil.BotCheckFailedError())
	fake := &clientstest.FakeCommodore{
		ResendVerificationFn: func(context.Context, string, string) (*commodorepb.ResendVerificationResponse, error) {
			return nil, botError
		},
		ForgotPasswordFn: func(context.Context, string, string) (*commodorepb.ForgotPasswordResponse, error) {
			return nil, botError
		},
	}
	h := &AuthHandlers{commodore: fake, logger: logging.NewLogger()}
	for path, handler := range map[string]gin.HandlerFunc{
		"/auth/resend-verification": h.ResendVerification(),
		"/auth/forgot-password":     h.ForgotPassword(),
	} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(`{"email":"user@example.com","turnstile_token":"invalid"}`))
		c.Request.Header.Set("Content-Type", "application/json")
		handler(c)
		if recorder.Code != http.StatusForbidden || !strings.Contains(recorder.Body.String(), "BOT_CHECK_FAILED") {
			t.Fatalf("%s response = %d %s", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestRecoveryHandlersDoNotClaimDeliveryOnServiceFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &clientstest.FakeCommodore{
		ResendVerificationFn: func(context.Context, string, string) (*commodorepb.ResendVerificationResponse, error) {
			return nil, status.Error(codes.Unavailable, "private upstream")
		},
		ForgotPasswordFn: func(context.Context, string, string) (*commodorepb.ForgotPasswordResponse, error) {
			return nil, status.Error(codes.PermissionDenied, "permission denied")
		},
	}
	h := &AuthHandlers{commodore: fake, logger: logging.NewLogger()}
	for path, handler := range map[string]gin.HandlerFunc{
		"/auth/resend-verification": h.ResendVerification(),
		"/auth/forgot-password":     h.ForgotPassword(),
	} {
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(`{"email":"user@example.com"}`))
		c.Request.Header.Set("Content-Type", "application/json")
		handler(c)
		if recorder.Code != http.StatusServiceUnavailable || strings.Contains(recorder.Body.String(), "will be sent") {
			t.Fatalf("%s response = %d %s", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestWalletChallengeAndLoginSessionCookies(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fake := &clientstest.FakeCommodore{
		IssueWalletChallengeFn: func(_ context.Context, address string, chainID uint64) (*commodorepb.IssueWalletChallengeResponse, error) {
			if address != "0xabc" || chainID != 1 {
				t.Fatalf("challenge request = %q %d", address, chainID)
			}
			return &commodorepb.IssueWalletChallengeResponse{Message: "server challenge", ExpiresAt: timestamppb.New(time.Now().Add(time.Minute))}, nil
		},
		WalletLoginFn: func(_ context.Context, address, message, signature string, _ *commonpb.SignupAttribution) (*commodorepb.AuthResponse, error) {
			return &commodorepb.AuthResponse{
				Token: "access", RefreshToken: "refresh",
				User:      &commodorepb.User{Id: "u1", TenantId: "t1"},
				ExpiresAt: timestamppb.New(time.Now().Add(15 * time.Minute)),
			}, nil
		},
	}
	h := &AuthHandlers{commodore: fake, logger: logging.NewLogger()}

	challengeRecorder := httptest.NewRecorder()
	challengeContext, _ := gin.CreateTestContext(challengeRecorder)
	challengeContext.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/wallet-challenge", strings.NewReader(`{"address":"0xabc","chain_id":1}`))
	challengeContext.Request.Header.Set("Content-Type", "application/json")
	h.WalletChallenge()(challengeContext)
	if challengeRecorder.Code != http.StatusOK || !strings.Contains(challengeRecorder.Body.String(), "server challenge") {
		t.Fatalf("challenge response = %d %s", challengeRecorder.Code, challengeRecorder.Body.String())
	}

	loginRecorder := httptest.NewRecorder()
	loginContext, _ := gin.CreateTestContext(loginRecorder)
	loginContext.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/auth/wallet-login", strings.NewReader(`{"address":"0xabc","message":"server challenge","signature":"0xsig"}`))
	loginContext.Request.Header.Set("Content-Type", "application/json")
	h.WalletLogin()(loginContext)
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("login response = %d %s", loginRecorder.Code, loginRecorder.Body.String())
	}
	cookies := loginRecorder.Result().Cookies()
	want := map[string]string{accessTokenCookie: "access", refreshTokenCookie: "refresh", tenantIDCookie: "t1"}
	for _, cookie := range cookies {
		if expected, ok := want[cookie.Name]; ok && cookie.Value == expected {
			delete(want, cookie.Name)
		}
	}
	if len(want) != 0 {
		t.Fatalf("missing wallet session cookies: %#v", want)
	}
}

// TestHandleBotCheckError checks the typed reason after gRPC sanitization,
// which removes the original PermissionDenied message.
func TestHandleBotCheckError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		err         error
		wantHandled bool
		wantStatus  int
		wantCode    string
	}{
		{
			name:        "bot check failure produces 403 + BOT_CHECK_FAILED",
			err:         grpcutil.SanitizeError(grpcutil.BotCheckFailedError()),
			wantHandled: true,
			wantStatus:  http.StatusForbidden,
			wantCode:    "BOT_CHECK_FAILED",
		},
		{
			name:        "plain bot-check message without typed reason passes through",
			err:         status.Error(codes.PermissionDenied, "bot verification failed"),
			wantHandled: false,
		},
		{
			name:        "other PermissionDenied passes through (not bot-check)",
			err:         status.Error(codes.PermissionDenied, "account suspended"),
			wantHandled: false,
		},
		{
			name:        "Unauthenticated passes through",
			err:         status.Error(codes.Unauthenticated, "invalid credentials"),
			wantHandled: false,
		},
		{
			name:        "plain error passes through",
			err:         errors.New("network sad"),
			wantHandled: false,
		},
		{
			name:        "nil passes through",
			err:         nil,
			wantHandled: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)

			got := handleBotCheckError(c, tc.err)
			if got != tc.wantHandled {
				t.Fatalf("handleBotCheckError() = %v, want %v", got, tc.wantHandled)
			}
			if !tc.wantHandled {
				if rec.Code != http.StatusOK && rec.Code != 0 {
					t.Fatalf("handler should not have written a response; got status %d", rec.Code)
				}
				return
			}
			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d, want %d", rec.Code, tc.wantStatus)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal body: %v (raw=%q)", err, rec.Body.String())
			}
			if body["error_code"] != tc.wantCode {
				t.Fatalf("error_code: got %q, want %q", body["error_code"], tc.wantCode)
			}
			if body["error"] == "" {
				t.Fatal("error message must be non-empty")
			}
		})
	}
}

func TestHandleEmailNotVerifiedLoginStatusError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		err         error
		wantHandled bool
	}{
		{
			name:        "sanitized typed verification error gets stable code",
			err:         grpcutil.SanitizeError(grpcutil.EmailNotVerifiedError()),
			wantHandled: true,
		},
		{
			name: "plain message is not a verification signal",
			err:  grpcutil.SanitizeError(status.Error(codes.Unauthenticated, "email not verified")),
		},
		{
			name:        "invalid credentials remains generic",
			err:         status.Error(codes.Unauthenticated, "invalid credentials"),
			wantHandled: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)

			got := handleEmailNotVerifiedLoginStatusError(c, tc.err)
			if got != tc.wantHandled {
				t.Fatalf("handleEmailNotVerifiedLoginStatusError() = %v, want %v", got, tc.wantHandled)
			}
			if !tc.wantHandled {
				if rec.Code != http.StatusOK && rec.Code != 0 {
					t.Fatalf("handler should not have written a response; got status %d", rec.Code)
				}
				return
			}
			if rec.Code != http.StatusForbidden {
				t.Fatalf("status: got %d, want %d", rec.Code, http.StatusForbidden)
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal body: %v (raw=%q)", err, rec.Body.String())
			}
			if body["error_code"] != emailNotVerifiedErrorCode {
				t.Fatalf("error_code: got %q, want %q", body["error_code"], emailNotVerifiedErrorCode)
			}
			if body["error"] != "email not verified" {
				t.Fatalf("error: got %q, want %q", body["error"], "email not verified")
			}
		})
	}
}

func TestLoginMapsActivationErrorsToVerificationResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantError  string
		wantCode   string
	}{
		{
			name:       "email not verified",
			err:        grpcutil.SanitizeError(grpcutil.EmailNotVerifiedError()),
			wantStatus: http.StatusForbidden,
			wantError:  "email not verified",
			wantCode:   emailNotVerifiedErrorCode,
		},
		{
			name:       "plain verification message is not trusted",
			err:        grpcutil.SanitizeError(status.Error(codes.Unauthenticated, "email not verified")),
			wantStatus: http.StatusUnauthorized,
			wantError:  "invalid credentials",
		},
		{
			name:       "invalid credentials",
			err:        status.Error(codes.Unauthenticated, "invalid credentials"),
			wantStatus: http.StatusUnauthorized,
			wantError:  "invalid credentials",
		},
		{
			name:       "deactivated account is not verification",
			err:        status.Error(codes.Unauthenticated, "account deactivated"),
			wantStatus: http.StatusUnauthorized,
			wantError:  "account deactivated",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &AuthHandlers{
				commodore: &clientstest.FakeCommodore{
					LoginFn: func(_ context.Context, req *commodorepb.LoginRequest) (*commodorepb.AuthResponse, error) {
						if req.Email != "user@example.com" || req.Password != "correct-password" {
							t.Fatalf("login request = %+v", req)
						}
						return nil, tc.err
					},
				},
				logger: logging.NewLogger(),
			}

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequestWithContext(
				context.Background(),
				http.MethodPost,
				"/auth/login",
				strings.NewReader(`{"email":"user@example.com","password":"correct-password"}`),
			)
			c.Request.Header.Set("Content-Type", "application/json")

			h.Login()(c)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d, want %d (body=%q)", rec.Code, tc.wantStatus, rec.Body.String())
			}
			var body map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("unmarshal body: %v (raw=%q)", err, rec.Body.String())
			}
			if body["error"] != tc.wantError {
				t.Fatalf("error: got %q, want %q", body["error"], tc.wantError)
			}
			if body["error_code"] != tc.wantCode {
				t.Fatalf("error_code: got %q, want %q", body["error_code"], tc.wantCode)
			}
		})
	}
}

// TestRefreshToken_ErrorMapping locks the rule that only a definitive
// Unauthenticated from Commodore may clear the auth cookies. Clearing on
// transient errors (or on the losing side of a concurrent refresh) wipes the
// valid cookies a winning refresh just set and logs the user out everywhere.
func TestRefreshToken_ErrorMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name           string
		err            error
		wantStatus     int
		wantClearedJar bool
	}{
		{
			name:           "unauthenticated clears cookies",
			err:            status.Error(codes.Unauthenticated, "invalid or expired refresh token"),
			wantStatus:     http.StatusUnauthorized,
			wantClearedJar: true,
		},
		{
			name:           "internal error keeps cookies",
			err:            status.Error(codes.Internal, "database error"),
			wantStatus:     http.StatusServiceUnavailable,
			wantClearedJar: false,
		},
		{
			name:           "unavailable keeps cookies",
			err:            status.Error(codes.Unavailable, "circuit breaker open"),
			wantStatus:     http.StatusServiceUnavailable,
			wantClearedJar: false,
		},
		{
			name:           "plain error keeps cookies",
			err:            errors.New("network sad"),
			wantStatus:     http.StatusServiceUnavailable,
			wantClearedJar: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := &AuthHandlers{
				commodore: &clientstest.FakeCommodore{
					RefreshTokenFn: func(_ context.Context, _ string) (*commodorepb.AuthResponse, error) {
						return nil, tc.err
					},
				},
				logger: logging.NewLogger(),
			}

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/refresh", nil)
			c.Request.AddCookie(&http.Cookie{Name: refreshTokenCookie, Value: "stale-token"})

			h.RefreshToken()(c)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d, want %d", rec.Code, tc.wantStatus)
			}
			cleared := false
			for _, cookie := range rec.Result().Cookies() {
				if cookie.Name == refreshTokenCookie && cookie.MaxAge < 0 {
					cleared = true
				}
			}
			if cleared != tc.wantClearedJar {
				t.Fatalf("refresh cookie cleared = %v, want %v", cleared, tc.wantClearedJar)
			}
		})
	}
}

func TestIsAuthServiceUnavailable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "plain error", err: errors.New("network sad"), want: false},
		{name: "unavailable", err: status.Error(codes.Unavailable, "circuit breaker open"), want: true},
		{name: "deadline", err: status.Error(codes.DeadlineExceeded, "timeout"), want: true},
		{name: "unauthenticated", err: status.Error(codes.Unauthenticated, "invalid credentials"), want: false},
		{name: "permission denied", err: status.Error(codes.PermissionDenied, "nope"), want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAuthServiceUnavailable(tc.err); got != tc.want {
				t.Fatalf("isAuthServiceUnavailable() = %v, want %v", got, tc.want)
			}
		})
	}
}
