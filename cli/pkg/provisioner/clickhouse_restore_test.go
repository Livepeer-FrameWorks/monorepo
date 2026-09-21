package provisioner

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	"frameworks/cli/pkg/ssh"
)

type passwordCaptureRunner struct {
	command string
	input   string
}

func (r *passwordCaptureRunner) RunStream(_ context.Context, command string, in io.Reader, _ io.Writer) (*ssh.CommandResult, error) {
	r.command = command
	data, err := io.ReadAll(in)
	if err != nil {
		return nil, err
	}
	r.input = string(data)
	return nil, fmt.Errorf("command failed: %s", command)
}

func TestClickHousePasswordNeverAppearsInCommandErrors(t *testing.T) {
	password := "test-password-'-$-\\n-with-space "
	encoded := base64.StdEncoding.EncodeToString([]byte(password))
	runner := &passwordCaptureRunner{}
	_, err := (ClickHouseServer{Password: password}).runStream(context.Background(), runner, "clickhouse-client", strings.NewReader("dump-payload"), io.Discard)
	if err == nil || strings.Contains(err.Error(), password) || strings.Contains(err.Error(), encoded) {
		t.Fatal("command error exposed credentials")
	}
	if runner.input != encoded+"\n"+"dump-payload" {
		t.Fatal("password framing consumed or changed the dump payload")
	}
	if !strings.Contains(runner.command, "CLICKHOUSE_PASSWORD") {
		t.Fatal("password was not exported for the client")
	}
}

// restoreTableRunner models the persisted tables across separate restore invocations.
type restoreTableRunner struct {
	tables   map[string][]string
	failure  string
	commands []string
}

func (r *restoreTableRunner) RunStream(_ context.Context, command string, _ io.Reader, out io.Writer) (*ssh.CommandResult, error) {
	_, quoted, _ := strings.Cut(command, "--query ")
	query := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(quoted, "'"), "'"), "'\\''", "'")
	r.commands = append(r.commands, query)
	refs := regexp.MustCompile("`periscope`\\.`([^`]+)`").FindAllStringSubmatch(query, -1)
	var result string
	switch {
	case strings.Contains(query, "FROM system.tables"):
		match := regexp.MustCompile("name = '([^']+)'").FindStringSubmatch(query)
		_, present := r.tables[match[1]]
		result = "0"
		if present {
			result = "1"
		}
	case strings.Contains(query, "FROM system.columns"):
		result = "id\tUInt64"
	case strings.Contains(query, "FROM system.parts"):
		match := regexp.MustCompile("table = '([^']+)'").FindStringSubmatch(query)
		result = strings.Join(r.tables[match[1]], "\n")
	case strings.HasPrefix(query, "CREATE TABLE"):
		if _, exists := r.tables[refs[0][1]]; exists {
			return nil, errors.New("table already exists")
		}
		r.tables[refs[0][1]] = nil
	case strings.HasPrefix(query, "ALTER TABLE"):
		target := refs[0][1]
		partition := regexp.MustCompile("PARTITION ID '([^']+)'").FindStringSubmatch(query)[1]
		if (r.failure == "copy" && strings.HasSuffix(target, "_building") || r.failure == "swap" && target == "api_requests") && partition == "202608" {
			return nil, errors.New("injected partition failure")
		}
		if strings.Contains(query, "DROP PARTITION") {
			r.tables[target] = slices.DeleteFunc(r.tables[target], func(p string) bool { return p == partition })
		} else if !slices.Contains(r.tables[target], partition) {
			r.tables[target] = append(r.tables[target], partition)
			slices.Sort(r.tables[target])
		}
	case strings.Contains(query, "cityHash64"):
		result = strings.Join(r.tables[refs[0][1]], ",")
		if r.failure == "fingerprint" && strings.HasSuffix(refs[0][1], "_building") {
			result = "corrupted"
		}
	case strings.HasPrefix(query, "RENAME TABLE"):
		if r.failure == "publish-before" {
			return nil, errors.New("injected rename failure")
		}
		r.tables[refs[1][1]] = r.tables[refs[0][1]]
		delete(r.tables, refs[0][1])
		if r.failure == "publish-after" {
			return nil, errors.New("lost rename acknowledgement")
		}
	case strings.HasPrefix(query, "DROP TABLE"):
		delete(r.tables, refs[0][1])
	default:
		return nil, fmt.Errorf("unexpected query: %s", query)
	}
	_, err := io.WriteString(out, result)
	return &ssh.CommandResult{}, err
}

func TestClickHouseRollbackNeverUsesIncompletePreparation(t *testing.T) {
	for _, failure := range []string{"copy", "fingerprint", "publish-before"} {
		t.Run(failure, func(t *testing.T) {
			r := &restoreTableRunner{failure: failure, tables: map[string][]string{
				"api_requests": {"202607", "202608"}, ClickHouseShadowTable("api_requests"): {"202608"},
			}}
			c := ClickHouseServer{Database: "periscope"}
			ctx := context.Background()
			if err := SwapRestoredClickHouseTable(ctx, r, c, "api_requests"); err == nil {
				t.Fatal("expected preparation failure")
			}
			if _, exists := r.tables[ClickHousePreviousTable("api_requests")]; exists {
				t.Fatal("published an unverified rollback table")
			}
			if err := RollbackRestoredClickHouseTable(ctx, r, c, "api_requests"); err == nil {
				t.Fatal("rollback accepted an incomplete preparation")
			}
			if !reflect.DeepEqual(r.tables["api_requests"], []string{"202607", "202608"}) {
				t.Fatalf("live data changed: %v", r.tables)
			}
			if err := FinishRestoredClickHouseTable(ctx, r, c, "api_requests"); err != nil {
				t.Fatal(err)
			}
			if len(r.tables) != 1 {
				t.Fatalf("finish left temporary tables: %v", r.tables)
			}
		})
	}
}

func TestClickHousePublishedCopySurvivesInterruptedSwap(t *testing.T) {
	for _, failure := range []string{"publish-after", "swap", ""} {
		t.Run(failure, func(t *testing.T) {
			r := &restoreTableRunner{failure: failure, tables: map[string][]string{
				"api_requests": {"202607", "202608"}, ClickHouseShadowTable("api_requests"): {"202608"},
			}}
			c := ClickHouseServer{Database: "periscope"}
			ctx := context.Background()
			err := SwapRestoredClickHouseTable(ctx, r, c, "api_requests")
			if (err != nil) != (failure != "") {
				t.Fatalf("swap error = %v", err)
			}
			if !reflect.DeepEqual(r.tables[ClickHousePreviousTable("api_requests")], []string{"202607", "202608"}) {
				t.Fatalf("rollback copy is incomplete: %v", r.tables)
			}
			r.failure = ""
			if err := RollbackRestoredClickHouseTable(ctx, r, c, "api_requests"); err != nil {
				t.Fatal(err)
			}
			if len(r.tables) != 1 || !reflect.DeepEqual(r.tables["api_requests"], []string{"202607", "202608"}) {
				t.Fatalf("rollback did not recover original data: %v", r.tables)
			}
		})
	}
}
