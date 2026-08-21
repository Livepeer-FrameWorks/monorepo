//go:build schema_verify

package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func startPurserUsageRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-purser-usage-realpg-%d", time.Now().UnixNano())
	run := dockerpg.CLI
	t.Cleanup(func() { _, _ = run("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatalf("resolve PostgreSQL test image: %v", err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/purser.sql")
	if err != nil {
		t.Fatalf("read purser schema: %v", err)
	}
	if _, err = db.Exec(string(schema)); err != nil {
		t.Fatalf("apply purser schema: %v", err)
	}
	return db
}

func TestProcessUsageSummaryAbsentDimensions_RealPG(t *testing.T) {
	db := startPurserUsageRealPG(t)
	jm := &JobManager{db: db, logger: logging.NewLogger(), billing: &Service{}}
	start := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)
	end := start.Add(5 * time.Minute)
	summary := models.UsageSummary{
		TenantID: "11111111-1111-4111-8111-111111111111", ClusterID: "demo-media",
		SourceID: "periscope-local", ReportID: strings.Repeat("a", 64),
		PeriodStart: start, PeriodEnd: end,
		Meters: []models.MeterQuantity{{
			Meter: "peak_bandwidth_mbps", Unit: "megabit_per_second", Quantity: 0.058208,
		}},
		ProviderUsage: []models.ProviderUsage{{
			ProviderTenantID: "22222222-2222-4222-8222-222222222222", ProviderClusterID: "provider-cluster",
			Meter: models.MeterQuantity{Meter: "peak_bandwidth_mbps", Unit: "megabit_per_second", Quantity: 0.058208},
		}},
		UsageAdjustments: []models.UsageAdjustment{{
			SourceSystem: "test", SourceID: "adjustment-1", UsageType: "peak_bandwidth_mbps", Unit: "megabit_per_second",
			ClusterID: "demo-media", DeltaValue: -0.01, PeriodStart: start, PeriodEnd: end,
		}},
	}

	if _, err := jm.processUsageSummary(context.Background(), summary, "kafka-test"); err != nil {
		t.Fatalf("process usage summary: %v", err)
	}
	expectedHash := fmt.Sprintf("%x", sha256.Sum256([]byte("{}")))
	for _, table := range []string{"usage_records", "provider_usage_records", "usage_adjustments"} {
		var dimensions, dimensionKey string
		query := fmt.Sprintf(`SELECT dimensions::text, dimension_key FROM purser.%s LIMIT 1`, table)
		if err := db.QueryRowContext(context.Background(), query).Scan(&dimensions, &dimensionKey); err != nil {
			t.Fatalf("read %s: %v", table, err)
		}
		if dimensions != "{}" || dimensionKey != expectedHash {
			t.Fatalf("%s dimensions=%q key=%q, want {} / %s", table, dimensions, dimensionKey, expectedHash)
		}
	}
}
