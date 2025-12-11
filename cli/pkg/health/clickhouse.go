package health

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/ClickHouse/clickhouse-go/v2"
)

// ClickHouseChecker checks ClickHouse health
type ClickHouseChecker struct {
	User     string
	Password string
	Database string
}

// Check performs a health check on ClickHouse
func (c *ClickHouseChecker) Check(address string, port int) *CheckResult {
	result := &CheckResult{
		Name:      "clickhouse",
		CheckedAt: time.Now(),
		Metadata:  make(map[string]string),
	}

	start := time.Now()

	// Build connection string for ClickHouse
	dsn := fmt.Sprintf("clickhouse://%s:%s@%s:%d/%s?dial_timeout=5s&read_timeout=5s",
		c.User, c.Password, address, port, c.Database)

	db, err := sql.Open("clickhouse", dsn)
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

	// Get version
	var version string
	if err := db.QueryRow("SELECT version()").Scan(&version); err == nil {
		result.Metadata["version"] = version
	}

	// Get uptime
	var uptime int64
	if err := db.QueryRow("SELECT uptime()").Scan(&uptime); err == nil {
		result.Metadata["uptime_seconds"] = fmt.Sprintf("%d", uptime)
	}

	// Check number of tables
	var tableCount int
	query := fmt.Sprintf("SELECT COUNT(*) FROM system.tables WHERE database = '%s'", c.Database)
	if err := db.QueryRow(query).Scan(&tableCount); err == nil {
		result.Metadata["tables"] = fmt.Sprintf("%d", tableCount)
	}

	result.OK = true
	result.Status = "healthy"
	result.Message = fmt.Sprintf("Connected successfully (latency: %v)", result.Latency)

	return result
}
