package server

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/http/pprof"
	"net/netip"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
)

// RouterSpec configures NewServiceRouter for a service with typed
// configuration.
type RouterSpec struct {
	Service string
	Logger  logging.Logger
	Health  *monitoring.HealthChecker
	// Ready serves /ready. Nil serves the liveness checks at /ready.
	Ready   *monitoring.ReadinessChecker
	Metrics *monitoring.MetricsCollector
	Runtime config.HTTPRuntime
	// DebugToken enables /debug/pprof and /debug/config when non-empty. A
	// debug request must arrive directly from a private address, carry no
	// reverse-proxy forwarding headers, and present the token as a bearer.
	DebugToken string
	// DebugConfig returns the current typed configuration for /debug/config.
	DebugConfig        func() any
	DebugConfigOptions config.Options
}

// NewServiceRouter builds the standard service router: request ID, logging,
// recovery, CORS, and metrics middleware, plus /health, /ready, /metrics, and
// the guarded /debug surface.
func NewServiceRouter(spec RouterSpec) *gin.Engine {
	router := baseServiceRouter(spec.Logger, spec.Health, spec.Metrics, spec.Runtime.Release(), spec.Runtime.AllowedOrigins)
	readyHandler := spec.Health.Handler()
	if spec.Ready != nil {
		readyHandler = spec.Ready.Handler()
	}
	router.GET("/ready", readyHandler)
	router.HEAD("/ready", readyHandler)
	registerDebugRoutes(router, spec)
	return router
}

func baseServiceRouter(logger logging.Logger, healthChecker *monitoring.HealthChecker, metricsCollector *monitoring.MetricsCollector, release bool, allowedOrigins []string) *gin.Engine {
	if release {
		gin.SetMode(gin.ReleaseMode)
	}
	router := gin.New()
	router.RedirectTrailingSlash = false

	router.Use(middleware.RequestIDMiddleware())
	router.Use(middleware.LoggingMiddleware(logger))
	router.Use(middleware.RecoveryMiddleware(logger))
	router.Use(middleware.CORSMiddleware(allowedOrigins, !release))
	router.Use(metricsCollector.MetricsMiddleware())

	healthHandler := healthChecker.Handler()
	router.GET("/health", healthHandler)
	router.HEAD("/health", healthHandler)
	router.GET("/metrics", metricsCollector.Handler())
	router.NoRoute(func(c *gin.Context) {
		retryAlternateTrailingSlashPath(router, c)
	})
	return router
}

// proxyForwardingHeaders are set by every FrameWorks ingress render path
// (nginx and Caddy). Their presence means the request crossed a reverse proxy,
// so the private-address check on RemoteAddr no longer identifies the caller.
var proxyForwardingHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Real-IP"}

func registerDebugRoutes(router *gin.Engine, spec RouterSpec) {
	if strings.TrimSpace(spec.DebugToken) == "" {
		return
	}
	debug := router.Group("/debug", debugAccessGuard(spec.DebugToken))
	debug.Any("/pprof/*profile", func(c *gin.Context) {
		switch name := strings.TrimPrefix(c.Param("profile"), "/"); name {
		case "":
			pprof.Index(c.Writer, c.Request)
		case "cmdline":
			pprof.Cmdline(c.Writer, c.Request)
		case "profile":
			pprof.Profile(c.Writer, c.Request)
		case "symbol":
			pprof.Symbol(c.Writer, c.Request)
		case "trace":
			pprof.Trace(c.Writer, c.Request)
		default:
			pprof.Handler(name).ServeHTTP(c.Writer, c.Request)
		}
	})
	if spec.DebugConfig != nil {
		debug.GET("/config", func(c *gin.Context) {
			fields, err := config.Describe(spec.DebugConfig(), spec.DebugConfigOptions)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": "configuration could not be described"})
				return
			}
			body := gin.H{"service": spec.Service, "fields": fields}
			if failure := LastReloadFailure(); failure != nil {
				body["last_reload_failure"] = failure
			}
			c.JSON(http.StatusOK, body)
		})
	}
}

func debugAccessGuard(token string) gin.HandlerFunc {
	expected := []byte("Bearer " + token)
	return func(c *gin.Context) {
		for _, header := range proxyForwardingHeaders {
			if c.GetHeader(header) != "" {
				c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "debug endpoints are not served through a reverse proxy"})
				return
			}
		}
		if !RequirePrivateClient(c) {
			c.Abort()
			return
		}
		presented := []byte(strings.TrimSpace(c.GetHeader("Authorization")))
		if subtle.ConstantTimeCompare(presented, expected) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}

// RequirePrivateClient reports whether the direct peer is a loopback,
// private, or link-local address. Otherwise it writes 403 and returns false.
func RequirePrivateClient(c *gin.Context) bool {
	host := c.Request.RemoteAddr
	if splitHost, _, err := net.SplitHostPort(host); err == nil && splitHost != "" {
		host = splitHost
	}
	if IsPrivateClientIP(host) {
		return true
	}
	c.JSON(http.StatusForbidden, map[string]string{"error": "private network access required"})
	return false
}

// IsPrivateClientIP reports whether raw parses as a loopback, private, or
// link-local address.
func IsPrivateClientIP(raw string) bool {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast()
}
