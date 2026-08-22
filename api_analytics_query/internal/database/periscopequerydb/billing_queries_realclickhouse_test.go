//go:build schema_verify

package periscopequerydb

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

const (
	contractTenantID = "5eed517e-ba5e-da7a-517e-ba5eda7a0001"
	contractStreamID = "5eedfeed-11fe-ca57-feed-11feca570001"
)

type queryContract struct {
	statement Statement
	args      []any
}

func billingQueryContracts() []queryContract {
	start := time.Now().UTC().Add(-24 * time.Hour)
	end := time.Now().UTC().Add(time.Hour)
	startMS, endMS := start.UnixMilli(), end.UnixMilli()
	return []queryContract{
		{ActiveViewerReservations, nil},
		{ActiveTenants, []any{contractTenantID, "00000000-0000-0000-0000-000000000002", "00000000-0000-0000-0000-000000000003"}},
		{PeakBandwidth, []any{contractTenantID, start, end}},
		{MonthlyUniqueUsers, []any{contractTenantID, endMS, contractTenantID, startMS, endMS}},
		{APIUsageByDimension, []any{contractTenantID, start, end}},
		{ClusterStreamRuntime, []any{contractTenantID, start, end}},
		{ClusterProcessingSeconds, []any{contractTenantID, startMS, endMS, contractTenantID, startMS}},
		{ClusterStorageProviderUsage, []any{contractTenantID, endMS, startMS, endMS}},
		{UsageAdjustments, []any{startMS, endMS, contractTenantID}},
		{FirstViewerSessionProjection, []any{contractTenantID, "edge-node-1", "session-1"}},
		{FirstProcessingSegmentProjection, []any{contractTenantID, "edge-node-1", contractStreamID, "source-1"}},
		{FirstStreamSessionProjection, []any{contractTenantID, "edge-node-1", contractStreamID, "source-1"}},
		{FirstStorageProjection, []any{contractTenantID, "central-primary", "cold", contractTenantID, "central-primary", "s3", start.Format(time.RFC3339)}},
		{TenantViewerMetrics, []any{contractTenantID, startMS, endMS, contractTenantID, startMS}},
		{EarliestCanonicalBillingFact, []any{contractTenantID, contractTenantID, contractTenantID, contractTenantID, contractTenantID}},
	}
}

func TestBillingCatalogExecutesAgainstCurrentClickHouse(t *testing.T) {
	assertBillingCatalogComplete(t)
	db := startContractClickHouse(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	for _, contract := range billingQueryContracts() {
		contract := contract
		t.Run(contract.statement.Name(), func(t *testing.T) {
			rows, err := contract.statement.Query(ctx, db, contract.args...)
			if err != nil {
				t.Fatalf("execute: %v\n%s", err, contract.statement.SQL())
			}
			if err := rows.Close(); err != nil {
				t.Fatalf("close rows: %v", err)
			}
		})
	}
}

func assertBillingCatalogComplete(t *testing.T) {
	t.Helper()
	contracted := map[string]bool{}
	for _, contract := range billingQueryContracts() {
		if contracted[contract.statement.Name()] {
			t.Fatalf("duplicate contract %s", contract.statement.Name())
		}
		contracted[contract.statement.Name()] = true
	}
	file, err := parser.ParseFile(token.NewFileSet(), "billing_queries.go", nil, 0)
	if err != nil {
		t.Fatalf("parse billing catalog: %v", err)
	}
	defined := map[string]bool{}
	ast.Inspect(file, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok || ident.Name != "statement" || len(call.Args) == 0 {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if ok {
			defined[strings.Trim(literal.Value, `"`)] = true
		}
		return true
	})
	var missing, stale []string
	for name := range defined {
		if !contracted[name] {
			missing = append(missing, name)
		}
	}
	for name := range contracted {
		if !defined[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 || len(stale) > 0 {
		t.Fatalf("billing query contract drift: missing=%v stale=%v", missing, stale)
	}
}

func startContractClickHouse(t *testing.T) *sql.DB {
	t.Helper()
	requireContractDocker(t)
	name := fmt.Sprintf("fw-periscope-query-%d-%d", os.Getpid(), time.Now().UnixNano())
	root := contractRepositoryRoot(t)
	image := contractClickHouseImage(t, root)
	config := filepath.Join(root, "infrastructure", "clickhouse", "config.xml")
	if output, err := contractDocker(t, "", "run", "-d", "--name", name,
		"-e", "CLICKHOUSE_SKIP_USER_SETUP=1", "-p", "127.0.0.1::9000",
		"-v", config+":/etc/clickhouse-server/config.d/zz-keeper.xml:ro", image); err != nil {
		t.Fatalf("start ClickHouse: %v\n%s", err, output)
	}
	t.Cleanup(func() { _, _ = contractDocker(t, "", "rm", "-f", name) })
	waitForContractClickHouse(t, name)
	for _, path := range []string{"clickhouse/periscope.sql", "seeds/demo/clickhouse_demo_data.sql"} {
		content, err := dbsql.Content.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if output, err := contractDocker(t, string(content), "exec", "-i", name, "clickhouse-client", "--multiquery"); err != nil {
			t.Fatalf("apply %s: %v\n%s", path, err, output)
		}
	}
	port, err := contractDocker(t, "", "port", name, "9000/tcp")
	if err != nil {
		t.Fatalf("resolve ClickHouse port: %v", err)
	}
	db := clickhouse.OpenDB(&clickhouse.Options{
		Addr:        []string{strings.TrimSpace(port)},
		Auth:        clickhouse.Auth{Database: "periscope", Username: "default"},
		DialTimeout: 10 * time.Second,
	})
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping ClickHouse: %v", err)
	}
	return db
}

func requireContractDocker(t *testing.T) {
	t.Helper()
	if output, err := contractDocker(t, "", "version", "--format", "{{.Server.Version}}"); err != nil {
		t.Fatalf("Docker unavailable: %v: %s", err, output)
	}
}

func waitForContractClickHouse(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := contractDocker(t, "", "exec", name, "clickhouse-client", "--query", "SELECT 1"); err == nil {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("ClickHouse did not become ready")
}

func contractDocker(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	return output.String(), err
}

func contractRepositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}

func contractClickHouseImage(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "config", "infrastructure.yaml"))
	if err != nil {
		t.Fatalf("read infrastructure image authority: %v", err)
	}
	var image, digest string
	inClickHouse := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- name:") {
			inClickHouse = strings.TrimSpace(strings.TrimPrefix(trimmed, "- name:")) == "clickhouse"
			continue
		}
		if !inClickHouse {
			continue
		}
		if strings.HasPrefix(trimmed, "image:") {
			image = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "image:")), "\"'")
		}
		if strings.HasPrefix(trimmed, "digest:") {
			digest = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "digest:")), "\"'")
		}
	}
	if image == "" || digest == "" {
		t.Fatal("ClickHouse image authority is incomplete")
	}
	return image + "@" + digest
}
