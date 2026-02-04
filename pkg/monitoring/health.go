package monitoring

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/twmb/franz-go/pkg/kgo"
)

// HealthStatus represents the overall health status
type HealthStatus struct {
	Status    string                 `json:"status"`
	Service   string                 `json:"service"`
	Version   string                 `json:"version"`
	Timestamp int64                  `json:"timestamp"`
	Checks    map[string]CheckResult `json:"checks"`
}

const (
	StatusHealthy   = "healthy"
	StatusDegraded  = "degraded"
	StatusUnhealthy = "unhealthy"
)

// CheckResult represents the result of an individual health check
type CheckResult struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
	Latency string `json:"latency,omitempty"`
}

// HealthChecker manages and executes health checks
type HealthChecker struct {
	service string
	version string
	checks  map[string]HealthCheck
}

// HealthCheck is a function that performs a health check
type HealthCheck func() CheckResult

// NewHealthChecker creates a new health checker instance
func NewHealthChecker(service, version string) *HealthChecker {
	return &HealthChecker{
		service: service,
		version: version,
		checks:  make(map[string]HealthCheck),
	}
}

// AddCheck adds a health check to the checker
func (hc *HealthChecker) AddCheck(name string, check HealthCheck) {
	hc.checks[name] = check
}

// CheckHealth runs all health checks and returns the overall status
func (hc *HealthChecker) CheckHealth() HealthStatus {
	status := HealthStatus{
		Service:   hc.service,
		Version:   hc.version,
		Timestamp: time.Now().Unix(),
		Checks:    make(map[string]CheckResult),
	}

	anyUnhealthy := false
	anyDegraded := false
	for name, check := range hc.checks {
		result := check()
		status.Checks[name] = result
		switch result.Status {
		case StatusHealthy:
		case StatusDegraded:
			anyDegraded = true
		case StatusUnhealthy:
			anyUnhealthy = true
		default:
			anyUnhealthy = true
		}
	}

	switch {
	case anyUnhealthy:
		status.Status = StatusUnhealthy
	case anyDegraded:
		status.Status = StatusDegraded
	default:
		status.Status = StatusHealthy
	}

	return status
}

// Handler returns a middleware handler for the health check endpoint
func (hc *HealthChecker) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		health := hc.CheckHealth()
		statusCode := http.StatusOK
		if health.Status == StatusUnhealthy {
			statusCode = http.StatusServiceUnavailable
		}
		c.JSON(statusCode, health)
	}
}

// Common Health Check Functions

// DatabaseHealthCheck creates a health check for database connectivity
func DatabaseHealthCheck(db *sql.DB) HealthCheck {
	return func() CheckResult {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := db.PingContext(ctx)
		duration := time.Since(start)

		if err != nil {
			return CheckResult{
				Status:  "unhealthy",
				Message: fmt.Sprintf("Database ping failed: %v", err),
				Latency: duration.String(),
			}
		}

		return CheckResult{
			Status:  "healthy",
			Message: "Database connection successful",
			Latency: duration.String(),
		}
	}
}

// KafkaProducerHealthCheck creates a health check for Kafka producer using franz-go
func KafkaProducerHealthCheck(client *kgo.Client) HealthCheck {
	return func() CheckResult {
		start := time.Now()

		if client == nil {
			return CheckResult{
				Status:  "unhealthy",
				Message: "Kafka client is nil",
				Latency: time.Since(start).String(),
			}
		}

		// Check broker connectivity using Ping
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := client.Ping(ctx)
		duration := time.Since(start)

		if err != nil {
			return CheckResult{
				Status:  "unhealthy",
				Message: fmt.Sprintf("Kafka ping failed: %v", err),
				Latency: duration.String(),
			}
		}

		return CheckResult{
			Status:  "healthy",
			Message: "Kafka producer connection healthy",
			Latency: duration.String(),
		}
	}
}

// KafkaConsumerHealthCheck creates a health check for Kafka consumer using franz-go
func KafkaConsumerHealthCheck(client *kgo.Client) HealthCheck {
	return func() CheckResult {
		start := time.Now()

		if client == nil {
			return CheckResult{
				Status:  "unhealthy",
				Message: "Kafka consumer client is nil",
				Latency: time.Since(start).String(),
			}
		}

		// Check broker connectivity using Ping
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := client.Ping(ctx)
		duration := time.Since(start)

		if err != nil {
			return CheckResult{
				Status:  "unhealthy",
				Message: fmt.Sprintf("Kafka consumer ping failed: %v", err),
				Latency: duration.String(),
			}
		}

		return CheckResult{
			Status:  "healthy",
			Message: "Kafka consumer connection healthy",
			Latency: duration.String(),
		}
	}
}

// HTTPServiceHealthCheck creates a health check for HTTP service dependencies
func HTTPServiceHealthCheck(serviceName, url string) HealthCheck {
	return func() CheckResult {
		start := time.Now()
		client := &http.Client{
			Timeout: 5 * time.Second,
		}

		resp, err := client.Get(url)
		duration := time.Since(start)

		if err != nil {
			return CheckResult{
				Status:  "unhealthy",
				Message: fmt.Sprintf("%s service unreachable: %v", serviceName, err),
				Latency: duration.String(),
			}
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 400 {
			return CheckResult{
				Status:  "unhealthy",
				Message: fmt.Sprintf("%s service returned %d", serviceName, resp.StatusCode),
				Latency: duration.String(),
			}
		}

		return CheckResult{
			Status:  "healthy",
			Message: fmt.Sprintf("%s service responding", serviceName),
			Latency: duration.String(),
		}
	}
}

// ConfigurationHealthCheck creates a health check for required configuration
func ConfigurationHealthCheck(configs map[string]string) HealthCheck {
	return func() CheckResult {
		start := time.Now()
		missing := []string{}

		for key, value := range configs {
			if value == "" {
				missing = append(missing, key)
			}
		}

		if len(missing) > 0 {
			return CheckResult{
				Status:  "unhealthy",
				Message: fmt.Sprintf("Missing required configuration: %v", missing),
				Latency: time.Since(start).String(),
			}
		}

		return CheckResult{
			Status:  "healthy",
			Message: "All required configuration present",
			Latency: time.Since(start).String(),
		}
	}
}

// ClickHouseHealthCheck creates a health check for ClickHouse SQL connection
func ClickHouseHealthCheck(db *sql.DB) HealthCheck {
	return func() CheckResult {
		start := time.Now()

		if db == nil {
			return CheckResult{
				Status:  "unhealthy",
				Message: "ClickHouse connection is nil",
				Latency: time.Since(start).String(),
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := db.PingContext(ctx)
		duration := time.Since(start)

		if err != nil {
			return CheckResult{
				Status:  "unhealthy",
				Message: fmt.Sprintf("ClickHouse ping failed: %v", err),
				Latency: duration.String(),
			}
		}

		return CheckResult{
			Status:  "healthy",
			Message: "ClickHouse connection healthy",
			Latency: duration.String(),
		}
	}
}

// ClickHouseNativeHealthCheck creates a health check for ClickHouse native connection
func ClickHouseNativeHealthCheck(conn interface{ Ping(context.Context) error }) HealthCheck {
	return func() CheckResult {
		start := time.Now()

		if conn == nil {
			return CheckResult{
				Status:  "unhealthy",
				Message: "ClickHouse native connection is nil",
				Latency: time.Since(start).String(),
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		err := conn.Ping(ctx)
		duration := time.Since(start)

		if err != nil {
			return CheckResult{
				Status:  "unhealthy",
				Message: fmt.Sprintf("ClickHouse native ping failed: %v", err),
				Latency: duration.String(),
			}
		}

		return CheckResult{
			Status:  "healthy",
			Message: "ClickHouse native connection healthy",
			Latency: duration.String(),
		}
	}
}
