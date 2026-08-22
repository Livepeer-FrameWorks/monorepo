//go:build schema_verify

package periscopeingestdb

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/google/uuid"
)

type writerContract struct {
	name    string
	prepare any
}

var writerContracts = []writerContract{
	{"PrepareAPIEvent", PrepareAPIEvent},
	{"PrepareAPIRequest", PrepareAPIRequest},
	{"PrepareArtifactNodeCopyCurrent", PrepareArtifactNodeCopyCurrent},
	{"PrepareArtifactNodeCopyEvent", PrepareArtifactNodeCopyEvent},
	{"PrepareClientQOESample", PrepareClientQOESample},
	{"PrepareClientQOESessionDelta", PrepareClientQOESessionDelta},
	{"PrepareClipArtifactEvent", PrepareClipArtifactEvent},
	{"PrepareClipArtifactState", PrepareClipArtifactState},
	{"PrepareDVRArtifactEvent", PrepareDVRArtifactEvent},
	{"PrepareDVRArtifactState", PrepareDVRArtifactState},
	{"PrepareFederationEvent", PrepareFederationEvent},
	{"PrepareIngestError", PrepareIngestError},
	{"PrepareLedgerRebuildCursor", PrepareLedgerRebuildCursor},
	{"PrepareNodeMetricsSample", PrepareNodeMetricsSample},
	{"PrepareNodeState", PrepareNodeState},
	{"PrepareOrchestratorAIOutcome", PrepareOrchestratorAIOutcome},
	{"PrepareOrchestratorDiscoverySample", PrepareOrchestratorDiscoverySample},
	{"PrepareOrchestratorInstanceStateCurrent", PrepareOrchestratorInstanceStateCurrent},
	{"PrepareOrchestratorStateCurrent", PrepareOrchestratorStateCurrent},
	{"PrepareOrchestratorTranscodeOutcome", PrepareOrchestratorTranscodeOutcome},
	{"PrepareOrchestratorVantageCurrent", PrepareOrchestratorVantageCurrent},
	{"PreparePlayerBootSample", PreparePlayerBootSample},
	{"PrepareProcessing5m", PrepareProcessing5m},
	{"PrepareProcessingEvent", PrepareProcessingEvent},
	{"PrepareProcessingSegmentFinal", PrepareProcessingSegmentFinal},
	{"PrepareProjectionDivergence", PrepareProjectionDivergence},
	{"PreparePushRewriteEvent", PreparePushRewriteEvent},
	{"PrepareRawMistTrigger", PrepareRawMistTrigger},
	{"PrepareRoutingDecision", PrepareRoutingDecision},
	{"PrepareStorageEvent", PrepareStorageEvent},
	{"PrepareStorageGBSeconds5m", PrepareStorageGBSeconds5m},
	{"PrepareStorageSnapshot", PrepareStorageSnapshot},
	{"PrepareStreamBufferEvent", PrepareStreamBufferEvent},
	{"PrepareStreamBufferHealth", PrepareStreamBufferHealth},
	{"PrepareStreamEndEvent", PrepareStreamEndEvent},
	{"PrepareStreamLifecycleEvent", PrepareStreamLifecycleEvent},
	{"PrepareStreamLifecycleHealth", PrepareStreamLifecycleHealth},
	{"PrepareStreamLifecycleState", PrepareStreamLifecycleState},
	{"PrepareStreamRuntime5m", PrepareStreamRuntime5m},
	{"PrepareStreamSessionAnomalous", PrepareStreamSessionAnomalous},
	{"PrepareStreamSessionFinal", PrepareStreamSessionFinal},
	{"PrepareTenantAcquisitionEvent", PrepareTenantAcquisitionEvent},
	{"PrepareTrackListEvent", PrepareTrackListEvent},
	{"PrepareTrackListStreamEvent", PrepareTrackListStreamEvent},
	{"PrepareVODArtifactEvent", PrepareVODArtifactEvent},
	{"PrepareVODArtifactState", PrepareVODArtifactState},
	{"PrepareVODRetentionBucket", PrepareVODRetentionBucket},
	{"PrepareViewerConnectionEvent", PrepareViewerConnectionEvent},
	{"PrepareViewerSessionAnomalous", PrepareViewerSessionAnomalous},
	{"PrepareViewerSessionFinal", PrepareViewerSessionFinal},
	{"PrepareViewerUsage5m", PrepareViewerUsage5m},
}

type nativeContractConn struct{ driver.Conn }

func (c nativeContractConn) PrepareBatch(ctx context.Context, query string) (Batch, error) {
	return c.Conn.PrepareBatch(ctx, query)
}

func TestEveryTypedWriterAppendsToCurrentClickHouse(t *testing.T) {
	requireDocker(t)
	assertEveryPrepareFunctionIsContracted(t)

	name := fmt.Sprintf("fw-periscope-writers-%d-%d", os.Getpid(), time.Now().UnixNano())
	root := repositoryRoot(t)
	image := clickHouseHarnessImage(t, root)
	config := filepath.Join(root, "infrastructure", "clickhouse", "config.xml")
	if _, err := dockerRun(t, "", "run", "-d", "--name", name,
		"-e", "CLICKHOUSE_SKIP_USER_SETUP=1", "-p", "127.0.0.1::9000",
		"-v", config+":/etc/clickhouse-server/config.d/zz-keeper.xml:ro", image); err != nil {
		t.Fatalf("start ClickHouse: %v", err)
	}
	t.Cleanup(func() { dockerRemove(t, name) })
	waitForClickHouse(t, name)

	baseline, err := dbsql.Content.ReadFile("clickhouse/periscope.sql")
	if err != nil {
		t.Fatalf("read ClickHouse baseline: %v", err)
	}
	if output, err := dockerRun(t, string(baseline), "exec", "-i", name, "clickhouse-client", "--multiquery"); err != nil {
		t.Fatalf("apply ClickHouse baseline: %v\n%s", err, output)
	}
	port, err := dockerRunTimeout(t, 30*time.Second, "", "port", name, "9000/tcp")
	if err != nil {
		t.Fatalf("resolve ClickHouse native port: %v", err)
	}
	address := strings.TrimSpace(port)
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{address},
		Auth:        clickhouse.Auth{Database: "periscope", Username: "default"},
		DialTimeout: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("open ClickHouse: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancelPing()
	if err := conn.Ping(pingCtx); err != nil {
		t.Fatalf("ping ClickHouse: %v", err)
	}

	ctx := context.Background()
	wrapped := nativeContractConn{Conn: conn}
	for _, contract := range writerContracts {
		contract := contract
		t.Run(contract.name, func(t *testing.T) {
			writerCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			prepare := reflect.ValueOf(contract.prepare)
			outputs := prepare.Call([]reflect.Value{reflect.ValueOf(writerCtx), reflect.ValueOf(BatchPreparer(wrapped))})
			if errValue := outputs[1].Interface(); errValue != nil {
				t.Fatalf("prepare: %v", errValue)
			}
			writer := outputs[0]
			appendMethod := writer.MethodByName("Append")
			rowType := appendMethod.Type().In(0)
			for _, includeNullableValues := range []bool{false, true} {
				row := representativeValue(rowType, includeNullableValues)
				if result := appendMethod.Call([]reflect.Value{row})[0].Interface(); result != nil {
					t.Fatalf("append (nullable values populated=%t): %v", includeNullableValues, result)
				}
			}
			if result := writer.MethodByName("Send").Call(nil)[0].Interface(); result != nil {
				t.Fatalf("send: %v", result)
			}
			writer.MethodByName("Close").Call(nil)
		})
	}
}

func representativeValue(typ reflect.Type, includeNullableValues bool) reflect.Value {
	value := reflect.New(typ).Elem()
	fillRepresentative(value, includeNullableValues)
	return value
}

func fillRepresentative(value reflect.Value, includeNullableValues bool) {
	if value.Type() == reflect.TypeOf(time.Time{}) {
		value.Set(reflect.ValueOf(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)))
		return
	}
	if value.Type() == reflect.TypeOf(uuid.UUID{}) {
		value.Set(reflect.ValueOf(uuid.MustParse("11111111-1111-4111-8111-111111111111")))
		return
	}
	switch value.Kind() {
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			fillRepresentative(value.Field(i), includeNullableValues)
		}
	case reflect.Pointer:
		if includeNullableValues {
			value.Set(reflect.New(value.Type().Elem()))
			fillRepresentative(value.Elem(), true)
		}
	case reflect.String:
		value.SetString("{}")
	case reflect.Slice:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			value.SetBytes([]byte("{}"))
		} else {
			value.Set(reflect.MakeSlice(value.Type(), 0, 0))
		}
	}
}

func assertEveryPrepareFunctionIsContracted(t *testing.T) {
	t.Helper()
	want := map[string]bool{}
	for _, contract := range writerContracts {
		if want[contract.name] {
			t.Fatalf("duplicate writer contract %s", contract.name)
		}
		want[contract.name] = true
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read writer package: %v", err)
	}
	found := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_writers.go") {
			continue
		}
		file, parseErr := parser.ParseFile(token.NewFileSet(), entry.Name(), nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", entry.Name(), parseErr)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if ok && strings.HasPrefix(function.Name.Name, "Prepare") {
				found[function.Name.Name] = true
			}
		}
	}
	var missing, stale []string
	for name := range found {
		if !want[name] {
			missing = append(missing, name)
		}
	}
	for name := range want {
		if !found[name] {
			stale = append(stale, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) != 0 || len(stale) != 0 {
		t.Fatalf("writer contract registry drift: missing=%v stale=%v", missing, stale)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}

func clickHouseHarnessImage(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "config", "infrastructure.yaml"))
	if err != nil {
		t.Fatalf("read infrastructure image authority: %v", err)
	}
	lines := strings.Split(string(data), "\n")
	inClickHouse := false
	var image, digest string
	for _, line := range lines {
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

func dockerRun(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	return dockerRunTimeout(t, 3*time.Minute, stdin, args...)
}

func dockerRunTimeout(t *testing.T, timeout time.Duration, stdin string, args ...string) (string, error) {
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

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := dockerRunTimeout(t, 30*time.Second, "", "version", "--format", "{{.Server.Version}}"); err != nil {
		t.Fatalf("Docker unavailable: %v", err)
	}
}

func dockerRemove(t *testing.T, name string) {
	t.Helper()
	_, _ = dockerRunTimeout(t, 30*time.Second, "", "rm", "-fv", name)
}

func waitForClickHouse(t *testing.T, name string) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if output, err := dockerRunTimeout(t, 15*time.Second, "", "exec", name, "clickhouse-client", "-q", "SELECT 1"); err == nil && strings.TrimSpace(output) == "1" {
			return
		}
		time.Sleep(time.Second)
	}
	logs, _ := dockerRunTimeout(t, 30*time.Second, "", "logs", "--tail", "60", name)
	t.Fatalf("ClickHouse did not become ready:\n%s", logs)
}
