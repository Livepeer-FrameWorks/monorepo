package handlers

import (
	"context"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"frameworks/api_sidecar/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/gin-gonic/gin"
)

// MistAdminCookieName is the cookie carrying the operator's mint-issued
// session token after the /_mist/_session POST exchange. Scoped to
// Path=/_mist so it never leaks to /view or any tenant origin.
const MistAdminCookieName = "fw_mist_admin"

// MistAdminCookiePath constrains the cookie to the admin proxy mount.
const MistAdminCookiePath = "/_mist"

// Indirection so tests can supply fake validators without standing up a
// gRPC stream to Foghorn. Production code uses the control package's
// real implementations.
var (
	validateMistAdminSession = control.ValidateMistAdminSession
)

// MistAdminProxy returns a gin handler that reverse-proxies the request to
// the local Mist controller.
//
// The proxy is the auth boundary: requests are forwarded with no platform
// credentials and no forwarded-IP headers, so Mist's loopback auto-auth
// fires and Mist never sees the operator's session.
//
// Only loopback upstreams are supported. Docker edges, where Helmsman
// dials the mistserver container by service name, return 501 because that
// hop requires Mist's JSON challenge/response controller auth.
func MistAdminProxy(mistURL string, logger logging.Logger) gin.HandlerFunc {
	target, err := url.Parse(mistURL)
	if err != nil || target.Host == "" || target.Scheme == "" {
		logger.WithError(err).WithField("mist_url", mistURL).
			Error("mist admin proxy: invalid MISTSERVER_URL")
		return func(c *gin.Context) {
			c.String(http.StatusInternalServerError, "mist admin proxy misconfigured")
		}
	}

	if !isLoopbackHost(target.Hostname()) {
		logger.WithField("mist_host", target.Hostname()).
			Info("mist admin proxy disabled: upstream is non-loopback (docker edge); JSON auth flow required")
		return func(c *gin.Context) {
			c.String(http.StatusNotImplemented,
				"mist admin proxy is unsupported on this edge: upstream is non-loopback and requires Mist JSON challenge/response auth")
		}
	}

	proxy := newMistAdminReverseProxy(target)

	return func(c *gin.Context) {
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

// newMistAdminReverseProxy is split out so tests can exercise the rewrite
// rules against an arbitrary loopback upstream without going through the
// loopback gate in MistAdminProxy.
func newMistAdminReverseProxy(target *url.URL) *httputil.ReverseProxy {
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(target)

			// Strip the /_mist mount prefix. The LSP frontend derives its
			// API base from window.location and expects to reach the
			// controller at /api, /api2, /ws — so Helmsman owns the strip
			// (Caddy preserves /_mist intentionally).
			stripped := strings.TrimPrefix(pr.In.URL.Path, "/_mist")
			if stripped == "" {
				stripped = "/"
			}
			pr.Out.URL.Path = stripped
			pr.Out.URL.RawPath = ""
			pr.Out.URL.RawQuery = pr.In.URL.RawQuery

			// The proxy is the auth boundary — operator-platform JWTs and
			// session cookies must not reach Mist.
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("Cookie")

			// Mist's loopback auto-auth refuses the bypass if any of these
			// headers are present (it treats them as evidence of a
			// multi-hop chain).
			pr.Out.Header.Del("Forwarded")
			pr.Out.Header.Del("X-Forwarded-For")
			pr.Out.Header.Del("X-Forwarded-Host")
			pr.Out.Header.Del("X-Forwarded-Proto")
			pr.Out.Header.Del("X-Real-IP")
		},
		ModifyResponse: func(resp *http.Response) error {
			// Mist's controller sets its own session cookie on the same
			// eTLD+1 as the platform — strip it so it cannot collide with
			// platform cookies in the browser jar.
			resp.Header.Del("Set-Cookie")
			return nil
		},
	}
}

// isLoopbackHost reports whether the host portion of a URL is loopback
// (Mist's auto-auth bypass requirement). Accepts "localhost", any IP in
// 127.0.0.0/8, and ::1.
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// RequireMistAdmin gates the /_mist proxy. It accepts only the
// Commodore-minted cookie from /_mist/_session; direct bearer/API tokens
// are intentionally not accepted because they are not bound to this node.
func RequireMistAdmin(logger logging.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cookie, err := c.Cookie(MistAdminCookieName); err == nil && cookie != "" {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
			defer cancel()
			resp, err := validateMistAdminSession(ctx, cookie)
			if err == nil && resp != nil && resp.GetValid() && resp.GetExpiresAt() > time.Now().Unix() {
				c.Set("mist_admin_user_id", resp.GetUserId())
				c.Set("mist_admin_tenant_id", resp.GetTenantId())
				c.Set("mist_admin_role", resp.GetRole())
				c.Next()
				return
			}
			logger.WithError(err).Debug("mist admin cookie rejected")
		}

		c.String(http.StatusUnauthorized, "unauthorized")
		c.Abort()
	}
}

// MistAdminSessionHandler is the POST /_mist/_session endpoint that
// exchanges a short-lived session token (minted by the Gateway resolver
// via Commodore.MintMistAdminSession) for a cookie scoped to /_mist.
// Tokens arrive in the POST body — never query string — so they don't
// land in URL bars, referrers, or access logs.
//
// On success: sets fw_mist_admin (HttpOnly; Secure; SameSite=Lax;
// Path=/_mist; Max-Age aligned with the token's JWT exp) and 302s to
// /_mist/ where the LSP UI lives.
func MistAdminSessionHandler(logger logging.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.Header("Allow", http.MethodPost)
			c.String(http.StatusMethodNotAllowed, "POST only")
			return
		}
		if err := c.Request.ParseForm(); err != nil {
			c.String(http.StatusBadRequest, "invalid form")
			return
		}
		token := strings.TrimSpace(c.Request.PostForm.Get("session_token"))
		if token == "" {
			c.String(http.StatusBadRequest, "session_token required")
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		resp, err := validateMistAdminSession(ctx, token)
		if err != nil || resp == nil || !resp.GetValid() {
			logger.WithError(err).Info("mist admin session exchange rejected")
			c.String(http.StatusUnauthorized, "invalid or expired session token")
			return
		}

		maxAge := 0
		if exp := resp.GetExpiresAt(); exp > 0 {
			if remaining := exp - time.Now().Unix(); remaining > 0 {
				maxAge = int(remaining)
			}
		}
		if maxAge <= 0 {
			c.String(http.StatusUnauthorized, "session already expired")
			return
		}

		// secure=true is honored by browsers only over HTTPS — in dev with
		// the bootstrap Caddy "tls internal" cert, browsers still treat
		// the connection as secure. In tests the value is asserted directly.
		c.SetSameSite(http.SameSiteLaxMode)
		c.SetCookie(MistAdminCookieName, token, maxAge, MistAdminCookiePath, "", true, true)
		c.Redirect(http.StatusFound, MistAdminCookiePath+"/")
	}
}
