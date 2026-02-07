package health

import (
	"fmt"
	"net"
	"time"
)

// TCPChecker checks raw TCP connectivity.
type TCPChecker struct {
	Timeout time.Duration
}

// Check performs a TCP health check on a port.
func (c *TCPChecker) Check(address string, port int) *CheckResult {
	result := &CheckResult{
		Name:      "tcp",
		CheckedAt: time.Now(),
		Metadata:  make(map[string]string),
	}

	timeout := c.Timeout
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	target := fmt.Sprintf("%s:%d", address, port)
	result.Metadata["address"] = target

	start := time.Now()
	conn, err := net.DialTimeout("tcp", target, timeout)
	result.Latency = time.Since(start)
	if err != nil {
		result.OK = false
		result.Status = "unhealthy"
		result.Error = err.Error()
		return result
	}
	_ = conn.Close()

	result.OK = true
	result.Status = "healthy"
	result.Message = fmt.Sprintf("TCP connect OK (latency: %v)", result.Latency)
	return result
}
