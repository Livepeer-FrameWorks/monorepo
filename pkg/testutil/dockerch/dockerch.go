package dockerch

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// StartCurrent starts an isolated ClickHouse using the repository-pinned image
// and applies the current Periscope baseline. It is intended for schema_verify
// contracts that must exercise production ClickHouse behavior.
type Harness struct {
	Native database.ClickHouseNativeConn
	SQL    database.ClickHouseConn
}

func StartCurrent(t testing.TB, repositoryRoot, namePrefix string) Harness {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("%s-%d-%d", namePrefix, os.Getpid(), time.Now().UnixNano())
	image := harnessImage(t, repositoryRoot)
	config := filepath.Join(repositoryRoot, "infrastructure", "clickhouse", "config.xml")
	if _, err := run(t, 3*time.Minute, "", "run", "-d", "--name", name,
		"-e", "CLICKHOUSE_SKIP_USER_SETUP=1", "-p", "127.0.0.1::9000",
		"-v", config+":/etc/clickhouse-server/config.d/zz-keeper.xml:ro", image); err != nil {
		t.Fatalf("start ClickHouse: %v", err)
	}
	t.Cleanup(func() {
		if _, cleanupErr := run(t, 30*time.Second, "", "rm", "-fv", name); cleanupErr != nil {
			t.Errorf("remove ClickHouse container: %v", cleanupErr)
		}
	})
	waitReady(t, name)

	baseline, err := dbsql.Content.ReadFile("clickhouse/periscope.sql")
	if err != nil {
		t.Fatalf("read ClickHouse baseline: %v", err)
	}
	if output, applyErr := run(t, 3*time.Minute, string(baseline), "exec", "-i", name, "clickhouse-client", "--multiquery"); applyErr != nil {
		t.Fatalf("apply ClickHouse baseline: %v\n%s", applyErr, output)
	}
	port, err := run(t, 30*time.Second, "", "port", name, "9000/tcp")
	if err != nil {
		t.Fatalf("resolve ClickHouse native port: %v", err)
	}
	cfg := database.ClickHouseConfig{
		Addr: []string{strings.TrimSpace(port)}, Database: "periscope", Username: "default",
	}
	logger := logging.NewLogger()
	conn, err := database.ConnectClickHouseNative(cfg, logger)
	if err != nil {
		t.Fatalf("open ClickHouse: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	db, err := database.ConnectClickHouse(cfg, logger)
	if err != nil {
		t.Fatalf("open ClickHouse database/sql: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		t.Fatalf("ping ClickHouse: %v", err)
	}
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping ClickHouse database/sql: %v", err)
	}
	return Harness{Native: conn, SQL: db}
}

func harnessImage(t testing.TB, root string) string {
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
		switch {
		case strings.HasPrefix(trimmed, "image:"):
			image = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "image:")), "\"'")
		case strings.HasPrefix(trimmed, "digest:"):
			digest = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "digest:")), "\"'")
		}
	}
	if image == "" || digest == "" {
		t.Fatal("clickhouse image/digest absent from config/infrastructure.yaml")
	}
	return image + "@" + digest
}

func waitReady(t testing.TB, name string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if output, err := run(t, 15*time.Second, "", "exec", name, "clickhouse-client", "-q", "SELECT 1"); err == nil && strings.TrimSpace(output) == "1" {
			return
		}
		time.Sleep(time.Second)
	}
	logs, logsErr := run(t, 30*time.Second, "", "logs", "--tail", "60", name)
	if logsErr != nil {
		logs = fmt.Sprintf("logs unavailable: %v", logsErr)
	}
	t.Fatalf("ClickHouse did not become ready:\n%s", logs)
}

func run(t testing.TB, timeout time.Duration, stdin string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String(), nil
}
