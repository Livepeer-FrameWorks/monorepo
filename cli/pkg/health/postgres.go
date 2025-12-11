package health

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/lib/pq" // Postgres driver (also works with YugabyteDB)
)

// PostgresChecker checks Postgres/YugabyteDB health
type PostgresChecker struct {
	User     string
	Password string
	Database string
}

// Check performs a health check on Postgres/YugabyteDB
func (c *PostgresChecker) Check(address string, port int) *CheckResult {
	result := &CheckResult{
		Name:      "postgres",
		CheckedAt: time.Now(),
		Metadata:  make(map[string]string),
	}

	start := time.Now()

	// Build connection string
	connStr := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable connect_timeout=5",
		address, port, c.User, c.Password, c.Database)

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		result.OK = false
		result.Status = "unhealthy"
		result.Error = fmt.Sprintf("failed to open connection: %v", err)
		return result
	}
	defer db.Close()

	// Try to ping
	if err := db.Ping(); err != nil {
		result.OK = false
		result.Status = "unhealthy"
		result.Error = fmt.Sprintf("ping failed: %v", err)
		return result
	}

	result.Latency = time.Since(start)

	// Execute simple query
	var one int
	if err := db.QueryRow("SELECT 1").Scan(&one); err != nil {
		result.OK = false
		result.Status = "degraded"
		result.Error = fmt.Sprintf("query failed: %v", err)
		return result
	}

	// Check if it's YugabyteDB or vanilla Postgres
	var version string
	if err := db.QueryRow("SELECT version()").Scan(&version); err == nil {
		result.Metadata["version"] = version
		if contains(version, "YugabyteDB") {
			result.Metadata["type"] = "yugabyte"
		} else {
			result.Metadata["type"] = "postgres"
		}
	}

	// Check active connections
	var activeConns int
	if err := db.QueryRow("SELECT COUNT(*) FROM pg_stat_activity WHERE state = 'active'").Scan(&activeConns); err == nil {
		result.Metadata["active_connections"] = fmt.Sprintf("%d", activeConns)
	}

	result.OK = true
	result.Status = "healthy"
	result.Message = fmt.Sprintf("Connected successfully (latency: %v)", result.Latency)

	return result
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsInner(s, substr)))
}

func containsInner(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
