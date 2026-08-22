//go:build schema_verify

package grpc

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
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"frameworks/api_analytics_query/internal/database/periscopequerydb"
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	queryPackTenantID = "5eed517e-ba5e-da7a-517e-ba5eda7a0001"
	queryPackStreamID = "5eedfeed-11fe-ca57-feed-11feca570001"
)

func TestEveryRPCQuerySiteExecutesAgainstCurrentClickHouse(t *testing.T) {
	db := startQueryPackClickHouse(t)
	server := NewPeriscopeServer(db, logging.NewLogger())
	serviceCtx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	ctx, trace := periscopequerydb.WithTrace(serviceCtx)

	serverValue := reflect.ValueOf(server)
	serverType := serverValue.Type()
	for variant := 0; variant < 3; variant++ {
		for i := 0; i < serverType.NumMethod(); i++ {
			methodInfo := serverType.Method(i)
			method := serverValue.Method(i)
			if method.Type().NumIn() != 2 || method.Type().NumOut() != 2 {
				continue
			}
			requestType := method.Type().In(1)
			if requestType.Kind() != reflect.Pointer {
				continue
			}
			request := reflect.New(requestType.Elem())
			message, ok := request.Interface().(proto.Message)
			if !ok {
				continue
			}
			fillQueryPackMessage(message.ProtoReflect(), variant)
			t.Run(fmt.Sprintf("%s/variant=%d", methodInfo.Name, variant), func(t *testing.T) {
				callCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				defer cancel()
				outputs := method.Call([]reflect.Value{reflect.ValueOf(callCtx), request})
				if outputs[1].IsNil() {
					return
				}
				err := outputs[1].Interface().(error)
				switch status.Code(err) {
				case codes.InvalidArgument, codes.NotFound, codes.PermissionDenied:
					return
				default:
					t.Fatalf("RPC returned database/runtime failure: %v", err)
				}
			})
		}
	}

	if scanErrors := trace.Errors(); len(scanErrors) > 0 {
		t.Fatalf("real RPC pack observed result-shape scan failures: %v", scanErrors)
	}
	assertEveryQuerySiteReached(t, trace.Names())
}

func fillQueryPackMessage(message protoreflect.Message, variant int) {
	rich := variant == 1
	omitStream := variant == 2
	fields := message.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		name := string(field.Name())
		if field.IsList() {
			if field.Kind() == protoreflect.StringKind && (name == "stream_ids" || name == "tenant_ids" || name == "related_tenant_ids" || name == "cluster_ids" || name == "serving_cluster_ids") {
				value := queryPackTenantID
				if name == "stream_ids" {
					if omitStream {
						continue
					}
					value = queryPackStreamID
				} else if name == "cluster_ids" || name == "serving_cluster_ids" {
					value = "demo-media"
				}
				message.Mutable(field).List().Append(protoreflect.ValueOfString(value))
			}
			continue
		}
		if field.Kind() == protoreflect.MessageKind {
			nested := message.Mutable(field).Message()
			if string(field.Message().FullName()) == "google.protobuf.Timestamp" {
				seconds := time.Now().UTC().Add(time.Hour).Unix()
				if name == "start" || name == "day" || name == "cohort_month" {
					seconds = time.Now().UTC().Add(-7 * 24 * time.Hour).Unix()
				}
				nested.Set(field.Message().Fields().ByName("seconds"), protoreflect.ValueOfInt64(seconds))
				continue
			}
			fillQueryPackMessage(nested, variant)
			continue
		}
		switch field.Kind() {
		case protoreflect.StringKind:
			if name == "stream_id" && omitStream {
				continue
			}
			value, required := queryPackString(name)
			if required || rich {
				if value != "" {
					message.Set(field, protoreflect.ValueOfString(value))
				}
			}
		case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind:
			if name == "page_size" || name == "limit" || name == "days" || name == "bucket_minutes" {
				message.Set(field, protoreflect.ValueOfInt32(10))
			}
		case protoreflect.Uint32Kind, protoreflect.Fixed32Kind:
			if name == "page_size" || name == "limit" || name == "days" || name == "bucket_minutes" {
				message.Set(field, protoreflect.ValueOfUint32(10))
			}
		case protoreflect.BoolKind:
			if rich {
				message.Set(field, protoreflect.ValueOfBool(true))
			}
		}
	}
}

func queryPackString(name string) (string, bool) {
	switch name {
	case "tenant_id", "stream_tenant_id":
		return queryPackTenantID, true
	case "stream_id":
		return queryPackStreamID, true
	case "node_id":
		return "edge-node-1", false
	case "cluster_id":
		return "demo-media", false
	case "internal_name":
		return "demo_live_stream_001", false
	case "orch_addr":
		return "0x0000000000000000000000000000000000000001", true
	case "gateway_id":
		return "gateway-1", false
	case "resolved_ip":
		return "127.0.0.1", false
	case "request_id":
		return "demo-request", true
	case "artifact_hash":
		return "c3d4e5f678901234567890123456abcd", true
	case "interval":
		return "5m", false
	case "content_type":
		return "vod", false
	case "process_type":
		return "ffmpeg", false
	case "storage_scope":
		return "cold", false
	case "event_type":
		return "stream_start", false
	case "auth_type":
		return "jwt", false
	case "operation_type":
		return "query", false
	case "operation_name":
		return "GetStream", false
	default:
		return "", false
	}
}

func assertEveryQuerySiteReached(t *testing.T, traced map[string]int) {
	t.Helper()
	reached := map[string]bool{}
	for name := range traced {
		parts := strings.SplitN(name, ":", 3)
		if len(parts) >= 2 {
			reached[parts[0]+":"+parts[1]] = true
		}
	}
	var missing []string
	for _, filename := range []string{"server.go", "orchestrators.go"} {
		set := token.NewFileSet()
		file, err := parser.ParseFile(set, filename, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", filename, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "Query" && selector.Sel.Name != "QueryRow") {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok || pkg.Name != "periscopequerydb" {
				return true
			}
			position := set.Position(call.Pos())
			site := filepath.Base(position.Filename) + ":" + fmt.Sprint(position.Line)
			if !reached[site] {
				missing = append(missing, site)
			}
			return true
		})
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("real RPC pack did not reach query sites: %v", missing)
	}
}

func startQueryPackClickHouse(t *testing.T) *sql.DB {
	t.Helper()
	if output, err := queryPackDocker(t, "", "version", "--format", "{{.Server.Version}}"); err != nil {
		t.Fatalf("Docker unavailable: %v: %s", err, output)
	}
	name := fmt.Sprintf("fw-periscope-rpc-%d-%d", os.Getpid(), time.Now().UnixNano())
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	image := queryPackClickHouseImage(t, root)
	config := filepath.Join(root, "infrastructure", "clickhouse", "config.xml")
	if output, runErr := queryPackDocker(t, "", "run", "-d", "--name", name,
		"-e", "CLICKHOUSE_SKIP_USER_SETUP=1", "-p", "127.0.0.1::9000",
		"-v", config+":/etc/clickhouse-server/config.d/zz-keeper.xml:ro", image); runErr != nil {
		t.Fatalf("start ClickHouse: %v\n%s", runErr, output)
	}
	t.Cleanup(func() { _, _ = queryPackDocker(t, "", "rm", "-f", name) })
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		if _, execErr := queryPackDocker(t, "", "exec", name, "clickhouse-client", "--query", "SELECT 1"); execErr == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	for _, path := range []string{"clickhouse/periscope.sql", "seeds/demo/clickhouse_demo_data.sql"} {
		content, readErr := dbsql.Content.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		if output, applyErr := queryPackDocker(t, string(content), "exec", "-i", name, "clickhouse-client", "--multiquery"); applyErr != nil {
			t.Fatalf("apply %s: %v\n%s", path, applyErr, output)
		}
	}
	port, err := queryPackDocker(t, "", "port", name, "9000/tcp")
	if err != nil {
		t.Fatalf("resolve ClickHouse port: %v", err)
	}
	db := clickhouse.OpenDB(&clickhouse.Options{
		Addr:        []string{strings.TrimSpace(port)},
		Auth:        clickhouse.Auth{Database: "periscope", Username: "default"},
		DialTimeout: 10 * time.Second,
	})
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func queryPackDocker(t *testing.T, stdin string, args ...string) (string, error) {
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

func queryPackClickHouseImage(t *testing.T, root string) string {
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
