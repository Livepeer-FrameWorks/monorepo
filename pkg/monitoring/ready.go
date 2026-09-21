package monitoring

import (
	"net/http"
	"sync/atomic"

	"github.com/gin-gonic/gin"
)

// ReadinessChecker answers whether an instance can serve traffic, as distinct
// from HealthChecker's process liveness. It reports unhealthy while any
// dependency check is unhealthy and for the whole drain window once shutdown
// has started, so a caller routing on readiness stops sending new work before
// listeners close. Degraded checks still count as ready.
type ReadinessChecker struct {
	checks       *HealthChecker
	shuttingDown atomic.Bool
}

// NewReadinessChecker creates a readiness checker for a service.
func NewReadinessChecker(service, version string) *ReadinessChecker {
	return &ReadinessChecker{checks: NewHealthChecker(service, version)}
}

// AddCheck registers a dependency check. Register every check before the
// service starts serving; the check set is not safe for concurrent mutation.
func (r *ReadinessChecker) AddCheck(name string, check HealthCheck) {
	r.checks.AddCheck(name, check)
}

// MarkShuttingDown flips readiness to unhealthy for the rest of the process
// lifetime.
func (r *ReadinessChecker) MarkShuttingDown() {
	r.shuttingDown.Store(true)
}

// ShuttingDown reports whether shutdown has started.
func (r *ReadinessChecker) ShuttingDown() bool {
	return r.shuttingDown.Load()
}

// Check runs every dependency check and applies the shutdown state.
func (r *ReadinessChecker) Check() HealthStatus {
	status := r.checks.CheckHealth()
	if r.shuttingDown.Load() {
		status.Checks["shutdown"] = CheckResult{Status: StatusUnhealthy, Message: "instance is draining"}
		status.Status = StatusUnhealthy
	}
	return status
}

// Handler serves the readiness status: 200 when ready, 503 otherwise.
func (r *ReadinessChecker) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		status := r.Check()
		code := http.StatusOK
		if status.Status == StatusUnhealthy {
			code = http.StatusServiceUnavailable
		}
		c.JSON(code, status)
	}
}
