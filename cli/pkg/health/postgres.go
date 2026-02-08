package health

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/lib/pq" // Postgres driver (also works with YugabyteDB)
)

// PostgresChecker checks Postgres/YugabyteDB health
type PostgresChecker struct {
	User     string
	Password string
	Database string
}

// CheckDatabases confirms required databases exist and allow connections.
func (c *PostgresChecker) CheckDatabases(address string, port int, databases []string) *CheckResult {
	result := &CheckResult{
		Name:      "postgres_databases",
		CheckedAt: time.Now(),
		Metadata:  make(map[string]string),
	}

	if len(databases) == 0 {
		result.OK = true
		result.Status = "healthy"
		result.Message = "no databases specified"
		return result
	}

	start := time.Now()
	db, err := sql.Open("postgres", c.connString(address, port, c.Database))
	if err != nil {
		result.OK = false
		result.Status = "unhealthy"
		result.Error = fmt.Sprintf("failed to open connection: %v", err)
		return result
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	missing := []string{}
	denied := []string{}
	for _, name := range databases {
		var allow bool
		err := db.QueryRowContext(ctx, "SELECT datallowconn FROM pg_database WHERE datname = $1", name).Scan(&allow)
		if err == sql.ErrNoRows {
			missing = append(missing, name)
			continue
		}
		if err != nil {
			result.OK = false
			result.Status = "unhealthy"
			result.Error = fmt.Sprintf("query failed for %s: %v", name, err)
			return result
		}
		if !allow {
			denied = append(denied, name)
		}
	}

	result.Latency = time.Since(start)

	if len(missing) > 0 || len(denied) > 0 {
		result.OK = false
		result.Status = "unhealthy"
		if len(missing) > 0 {
			result.Metadata["missing"] = strings.Join(missing, ",")
		}
		if len(denied) > 0 {
			result.Metadata["disallowed"] = strings.Join(denied, ",")
		}
		result.Error = fmt.Sprintf("database readiness failed (missing: %v, disallowed: %v)", missing, denied)
		return result
	}

	result.OK = true
	result.Status = "healthy"
	result.Message = fmt.Sprintf("databases ready (latency: %v)", result.Latency)
	return result
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
	db, err := sql.Open("postgres", c.connString(address, port, c.Database))
	if err != nil {
		result.OK = false
		result.Status = "unhealthy"
		result.Error = fmt.Sprintf("failed to open connection: %v", err)
		return result
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Try to ping
	if err := db.PingContext(ctx); err != nil {
		result.OK = false
		result.Status = "unhealthy"
		result.Error = fmt.Sprintf("ping failed: %v", err)
		return result
	}

	result.Latency = time.Since(start)

	// Execute simple query
	var one int
	if err := db.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		result.OK = false
		result.Status = "degraded"
		result.Error = fmt.Sprintf("query failed: %v", err)
		return result
	}

	// Check if it's YugabyteDB or vanilla Postgres
	var version string
	if err := db.QueryRowContext(ctx, "SELECT version()").Scan(&version); err == nil {
		result.Metadata["version"] = version
		if contains(version, "YugabyteDB") {
			result.Metadata["type"] = "yugabyte"
		} else {
			result.Metadata["type"] = "postgres"
		}
	}

	// Check active connections
	var activeConns int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM pg_stat_activity WHERE state = 'active'").Scan(&activeConns); err == nil {
		result.Metadata["active_connections"] = fmt.Sprintf("%d", activeConns)
	}

	result.OK = true
	result.Status = "healthy"
	result.Message = fmt.Sprintf("Connected successfully (latency: %v)", result.Latency)

	return result
}

func (c *PostgresChecker) connString(address string, port int, database string) string {
	if database == "" {
		database = "postgres"
	}
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=disable connect_timeout=5",
		address, port, c.User, c.Password, database)
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
