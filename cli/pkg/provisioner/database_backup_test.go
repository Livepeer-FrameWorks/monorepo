package provisioner

import (
	"context"
	"io"
	"strings"
	"testing"

	"frameworks/cli/pkg/backup"
	"frameworks/cli/pkg/ssh"
)

func TestDumpDatabaseSectionCommand(t *testing.T) {
	pg, err := DumpDatabaseSectionCommand(DatabaseServer{Engine: backup.EnginePostgres, Port: 5432}, "purser", "data")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"bash -c", "set -o pipefail", "sudo -u postgres pg_dump -p 5432 -d", "--section=data", "gzip -c"} {
		if !strings.Contains(pg, want) {
			t.Fatalf("postgres dump command lacks %q:\n%s", want, pg)
		}
	}
	yb, err := DumpDatabaseSectionCommand(DatabaseServer{Engine: backup.EngineYugabyte, Port: 5433}, "foghorn_eu", "pre-data")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"fw_yb_bin ysql_dump", "-h localhost -p 5433 -U yugabyte", "--section=pre-data", "wrote to stderr", "exit 4"} {
		if !strings.Contains(yb, want) {
			t.Fatalf("yugabyte dump command lacks %q:\n%s", want, yb)
		}
	}
	for _, bad := range []struct {
		server   DatabaseServer
		db, sect string
	}{
		{DatabaseServer{Engine: backup.EnginePostgres, Port: 5432}, "purser; rm -rf /", "data"},
		{DatabaseServer{Engine: backup.EnginePostgres, Port: 5432}, "purser", "schema"},
		{DatabaseServer{Engine: "mysql", Port: 5432}, "purser", "data"},
		{DatabaseServer{Engine: backup.EnginePostgres}, "purser", "data"},
	} {
		if _, badErr := DumpDatabaseSectionCommand(bad.server, bad.db, bad.sect); badErr == nil {
			t.Fatalf("%+v %q %q must be rejected", bad.server, bad.db, bad.sect)
		}
	}
	restore, err := RestoreDatabaseSectionCommand(DatabaseServer{Engine: backup.EnginePostgres, Port: 5432}, "purser__restore")
	if err != nil || !strings.Contains(restore, "gunzip -c | sudo -u postgres psql -X -v ON_ERROR_STOP=1 -p 5432 -d") {
		t.Fatalf("restore command = %q, %v", restore, err)
	}
}

// scriptedRunner answers each command with the next canned output and records what ran.
type scriptedRunner struct {
	outputs  []string
	commands []string
}

func (s *scriptedRunner) RunStream(_ context.Context, command string, _ io.Reader, stdout io.Writer) (*ssh.CommandResult, error) {
	s.commands = append(s.commands, command)
	out := ""
	if len(s.outputs) > 0 {
		out, s.outputs = s.outputs[0], s.outputs[1:]
	}
	_, _ = io.WriteString(stdout, out) //nolint:errcheck // test runner
	return &ssh.CommandResult{Command: command}, nil
}

func TestReadDatabaseFactsAndLedger(t *testing.T) {
	s := DatabaseServer{Engine: backup.EnginePostgres, Port: 5432}
	r := &scriptedRunner{outputs: []string{
		"purser|UTF8|C.UTF-8|C.UTF-8\n",
		"CONNECT|PUBLIC\nCONNECT|purser_runtime\nCREATE|purser\n",
	}}
	facts, err := ReadDatabaseFacts(context.Background(), r, s, "purser")
	if err != nil {
		t.Fatal(err)
	}
	if !facts.Exists || facts.Owner != "purser" || facts.Encoding != "UTF8" || len(facts.Grants) != 3 || facts.Grants[1].Grantee != "purser_runtime" {
		t.Fatalf("facts = %+v", facts)
	}
	missing, err := ReadDatabaseFacts(context.Background(), &scriptedRunner{outputs: []string{""}}, s, "gone")
	if err != nil || missing.Exists {
		t.Fatalf("absent database: %+v %v", missing, err)
	}

	ledger, err := ReadDatabaseLedger(context.Background(), &scriptedRunner{outputs: []string{"t", "v0.3.10|expand|1|aaa\nv0.3.11|contract|2|bbb\n"}}, s, "purser")
	if err != nil || len(ledger) != 2 || ledger[1].Seq != 2 || ledger[1].Phase != "contract" {
		t.Fatalf("ledger = %+v, %v", ledger, err)
	}
	empty, err := ReadDatabaseLedger(context.Background(), &scriptedRunner{outputs: []string{"f"}}, s, "chatwoot")
	if err != nil || empty != nil {
		t.Fatalf("database without _migrations: %+v %v", empty, err)
	}
}

func TestCreateRestoreShadowRecreatesOwnerLocaleAndGrants(t *testing.T) {
	r := &scriptedRunner{}
	db := backup.Database{Name: "purser", Owner: "purser", Encoding: "UTF8", Collate: "C.UTF-8", Ctype: "C.UTF-8",
		Grants: []backup.DatabaseGrant{{Privilege: "CONNECT", Grantee: "purser_runtime"}, {Privilege: "TEMPORARY", Grantee: "PUBLIC"}}}
	if err := CreateRestoreShadow(context.Background(), r, DatabaseServer{Engine: backup.EnginePostgres, Port: 5432}, db, "purser__restore"); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(r.commands, "\n")
	for _, want := range []string{
		`DROP DATABASE IF EXISTS "purser__restore"`,
		`CREATE DATABASE "purser__restore" OWNER "purser" TEMPLATE template0 ENCODING`,
		`GRANT CONNECT ON DATABASE "purser__restore" TO "purser_runtime"`,
		`GRANT TEMPORARY ON DATABASE "purser__restore" TO PUBLIC`,
	} {
		if !strings.Contains(all, want) {
			t.Fatalf("shadow creation lacks %q:\n%s", want, all)
		}
	}
	bad := db
	bad.Grants = []backup.DatabaseGrant{{Privilege: "ALL; DROP DATABASE purser", Grantee: "x"}}
	if err := CreateRestoreShadow(context.Background(), &scriptedRunner{}, DatabaseServer{Engine: backup.EnginePostgres, Port: 5432}, bad, "purser__restore"); err == nil {
		t.Fatal("a privilege outside CREATE/CONNECT/TEMPORARY must be refused")
	}

	yb := &scriptedRunner{}
	if err := CreateRestoreShadow(context.Background(), yb, DatabaseServer{Engine: backup.EngineYugabyte, Port: 5433}, db, "purser__restore"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(yb.commands, "\n"), "TEMPLATE template0") {
		t.Fatal("YugabyteDB databases are created without locale options, as the yugabyte role creates them")
	}
}

func TestClickHouseClientQuotesPasswordAndQuery(t *testing.T) {
	c := ClickHouseServer{Port: 9000, Password: "p'w", Database: "periscope"}
	r := &scriptedRunner{outputs: []string{"a\tString\nb\tAggregateFunction(sum, UInt64)\n"}}
	cols, err := ClickHouseTableColumns(context.Background(), r, c, "api_requests")
	if err != nil || len(cols) != 2 || cols[1].Type != "AggregateFunction(sum, UInt64)" {
		t.Fatalf("columns = %+v, %v", cols, err)
	}
	if strings.Contains(r.commands[0], "p'w") || !strings.Contains(r.commands[0], `clickhouse-client --host 127.0.0.1 --port 9000 --database 'periscope'`) {
		t.Fatalf("command = %s", r.commands[0])
	}
	fp := &scriptedRunner{outputs: []string{"3\t42"}}
	got, err := ClickHouseFingerprint(context.Background(), fp, c, "t", cols)
	if err != nil || got != "3\t42" || !strings.Contains(fp.commands[0], "finalizeAggregation(`b`)") {
		t.Fatalf("fingerprint %q %v: %s", got, err, fp.commands[0])
	}
	if _, err := ClickHouseQuery(context.Background(), r, ClickHouseServer{Database: "bad-name"}, "SELECT 1"); err == nil {
		t.Fatal("an unsafe database name must be refused")
	}
}
