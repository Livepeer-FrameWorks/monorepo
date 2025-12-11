package health

import "time"

// CheckResult represents the result of a health check
type CheckResult struct {
	Name      string            `json:"name"`
	OK        bool              `json:"ok"`
	Status    string            `json:"status"` // healthy | degraded | unhealthy | unknown
	Message   string            `json:"message,omitempty"`
	Error     string            `json:"error,omitempty"`
	Latency   time.Duration     `json:"latency,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	CheckedAt time.Time         `json:"checked_at"`
}

// Checker performs health checks on services
type Checker interface {
	Check(address string, port int) *CheckResult
}
