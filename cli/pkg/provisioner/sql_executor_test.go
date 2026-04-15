package provisioner

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	fwssh "frameworks/cli/pkg/ssh"
)

func TestConnParamsConnStr(t *testing.T) {
	c := ConnParams{Host: "db.example.com", Port: 5432, User: "admin", Database: "mydb"}
	got := c.ConnStr()
	want := "host=db.example.com port=5432 user=admin dbname=mydb sslmode=disable"
	if got != want {
		t.Fatalf("ConnStr() = %q, want %q", got, want)
	}
}

func TestConnParamsConnStrSSLMode(t *testing.T) {
	c := ConnParams{Host: "db", Port: 5432, User: "u", Database: "d", SSLMode: "require"}
	got := c.ConnStr()
	want := "host=db port=5432 user=u dbname=d sslmode=require"
	if got != want {
		t.Fatalf("ConnStr() = %q, want %q", got, want)
	}
}

func TestInterpolateArgs(t *testing.T) {
	tests := []struct {
		query string
		args  []any
		want  string
	}{
		{
			query: "SELECT * FROM t WHERE id = $1",
			args:  []any{42},
			want:  "SELECT * FROM t WHERE id = 42",
		},
		{
			query: "SELECT * FROM t WHERE name = $1 AND id = $2",
			args:  []any{"o'reilly", 7},
			want:  "SELECT * FROM t WHERE name = 'o''reilly' AND id = 7",
		},
		{
			query: "SELECT 1",
			args:  nil,
			want:  "SELECT 1",
		},
		{
			query: "SELECT $1",
			args:  []any{true},
			want:  "SELECT TRUE",
		},
	}

	for _, tt := range tests {
		got, err := interpolateArgs(tt.query, tt.args)
		if err != nil {
			t.Fatalf("interpolateArgs(%q, %v) error: %v", tt.query, tt.args, err)
		}
		if got != tt.want {
			t.Errorf("interpolateArgs(%q, %v) = %q, want %q", tt.query, tt.args, got, tt.want)
		}
	}
}

func TestInterpolateArgsOutOfRange(t *testing.T) {
	_, err := interpolateArgs("SELECT $2", []any{"only_one"})
	if err == nil {
		t.Fatal("expected error for out-of-range parameter")
	}
}

func TestScanPsqlValue(t *testing.T) {
	var b bool
	if err := scanPsqlValue("t", &b); err != nil {
		t.Fatal(err)
	}
	if !b {
		t.Fatal("expected true")
	}

	if err := scanPsqlValue("f", &b); err != nil {
		t.Fatal(err)
	}
	if b {
		t.Fatal("expected false")
	}

	var s string
	if err := scanPsqlValue("hello", &s); err != nil {
		t.Fatal(err)
	}
	if s != "hello" {
		t.Fatalf("got %q, want %q", s, "hello")
	}

	var n int
	if err := scanPsqlValue("42", &n); err != nil {
		t.Fatal(err)
	}
	if n != 42 {
		t.Fatalf("got %d, want 42", n)
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"hello", "'hello'"},
		{"it's", "'it'\\''s'"},
		{"", "''"},
	}
	for _, tt := range tests {
		got := shellQuote(tt.input)
		if got != tt.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestQuoteLiteral(t *testing.T) {
	tests := []struct {
		input any
		want  string
	}{
		{42, "42"},
		{int64(99), "99"},
		{true, "TRUE"},
		{false, "FALSE"},
		{"hello", "'hello'"},
		{"it's", "'it''s'"},
	}
	for _, tt := range tests {
		got := quoteLiteral(tt.input)
		if got != tt.want {
			t.Errorf("quoteLiteral(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// SSHExecutor tests — verify upload-based approach, auth modes, quoting
// ---------------------------------------------------------------------------

func TestSSHExecutorExec_PeerAuth(t *testing.T) {
	runner := &mockRunner{}
	exec := &SSHExecutor{Runner: runner, UsePeerAuth: true}
	conn := ConnParams{Host: "db", Port: 5432, User: "postgres", Database: "mydb"}

	err := exec.Exec(context.Background(), conn, "CREATE TABLE t (id int)")
	if err != nil {
		t.Fatalf("Exec error: %v", err)
	}

	// SQL content should be in the uploaded file, not the command
	if runner.uploadedContent != "CREATE TABLE t (id int)" {
		t.Fatalf("uploaded content = %q, want SQL content", runner.uploadedContent)
	}
	if strings.Contains(runner.lastCmd, "CREATE TABLE") {
		t.Fatal("SQL content should not appear in shell command")
	}

	// Should use sudo -u, no -h flag
	if !strings.Contains(runner.lastCmd, "sudo -u 'postgres'") {
		t.Fatalf("expected sudo -u in command, got: %s", runner.lastCmd)
	}
	if strings.Contains(runner.lastCmd, "-h localhost") {
		t.Fatal("peer auth should not use -h localhost")
	}
	if !strings.Contains(runner.lastCmd, "-d 'mydb'") {
		t.Fatalf("expected quoted database in command, got: %s", runner.lastCmd)
	}
	if !strings.Contains(runner.lastCmd, "trap 'rm -f") {
		t.Fatal("expected trap for cleanup")
	}
}

func TestSSHExecutorExec_TCPAuth(t *testing.T) {
	runner := &mockRunner{}
	exec := &SSHExecutor{Runner: runner, UsePeerAuth: false, BinaryPath: "/opt/yugabyte/bin/ysqlsh"}
	conn := ConnParams{Host: "db", Port: 5433, User: "yugabyte", Database: "mydb"}

	err := exec.Exec(context.Background(), conn, "SELECT 1")
	if err != nil {
		t.Fatalf("Exec error: %v", err)
	}

	// Should use -h localhost and -U, no sudo
	if strings.Contains(runner.lastCmd, "sudo") {
		t.Fatal("TCP auth should not use sudo")
	}
	if !strings.Contains(runner.lastCmd, "-h localhost") {
		t.Fatalf("expected -h localhost, got: %s", runner.lastCmd)
	}
	if !strings.Contains(runner.lastCmd, "-U 'yugabyte'") {
		t.Fatalf("expected -U with quoted user, got: %s", runner.lastCmd)
	}
	if !strings.Contains(runner.lastCmd, "/opt/yugabyte/bin/ysqlsh") {
		t.Fatalf("expected custom binary path, got: %s", runner.lastCmd)
	}
}

func TestSSHExecutorExec_SQLNotInCommand(t *testing.T) {
	malicious := "'; DROP TABLE users; --"
	runner := &mockRunner{}
	exec := &SSHExecutor{Runner: runner, UsePeerAuth: true}
	conn := ConnParams{Host: "db", Port: 5432, User: "postgres", Database: "test"}

	_ = exec.Exec(context.Background(), conn, malicious)

	// SQL should be in the file, never in the command
	if runner.uploadedContent != malicious {
		t.Fatalf("uploaded content mismatch")
	}
	if strings.Contains(runner.lastCmd, "DROP TABLE") {
		t.Fatal("SQL content must not appear in shell command")
	}
}

func TestSSHExecutorQueryRow(t *testing.T) {
	runner := &mockRunner{stdout: "t\n"}
	exec := &SSHExecutor{Runner: runner, UsePeerAuth: true}
	conn := ConnParams{Host: "db", Port: 5432, User: "postgres", Database: "test"}

	var exists bool
	err := exec.QueryRow(context.Background(), conn, "SELECT EXISTS(SELECT 1)", nil, &exists)
	if err != nil {
		t.Fatalf("QueryRow error: %v", err)
	}
	if !exists {
		t.Fatal("expected true")
	}

	// Should have -tA flag for machine-parseable output
	if !strings.Contains(runner.lastCmd, "-tA") {
		t.Fatalf("expected -tA flag, got: %s", runner.lastCmd)
	}
}

func TestSSHExecutorQueryRowNoRows(t *testing.T) {
	runner := &mockRunner{stdout: ""}
	exec := &SSHExecutor{Runner: runner, UsePeerAuth: true}
	conn := ConnParams{Host: "db", Port: 5432, User: "postgres", Database: "test"}

	var s string
	err := exec.QueryRow(context.Background(), conn, "SELECT 1", nil, &s)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expected sql.ErrNoRows, got %v", err)
	}
}

func TestSSHExecutorQueryRowsMultiple(t *testing.T) {
	runner := &mockRunner{stdout: "v1.0.0|1\nv1.0.0|2\nv1.1.0|1\n"}
	exec := &SSHExecutor{Runner: runner, UsePeerAuth: true}
	conn := ConnParams{Host: "db", Port: 5432, User: "postgres", Database: "test"}

	type row struct {
		version string
		seq     int
	}
	var rows []row
	err := exec.QueryRows(context.Background(), conn, "SELECT version, seq FROM _migrations", nil, func(scan func(dest ...any) error) error {
		var r row
		if err := scan(&r.version, &r.seq); err != nil {
			return err
		}
		rows = append(rows, r)
		return nil
	})
	if err != nil {
		t.Fatalf("QueryRows error: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if rows[0].version != "v1.0.0" || rows[0].seq != 1 {
		t.Fatalf("unexpected row 0: %+v", rows[0])
	}
}

func TestSSHExecutorExecTx(t *testing.T) {
	runner := &mockRunner{}
	exec := &SSHExecutor{Runner: runner, UsePeerAuth: true}
	conn := ConnParams{Host: "db", Port: 5432, User: "postgres", Database: "test"}

	err := exec.ExecTx(context.Background(), conn, func(tx TxExecutor) error {
		if err := tx.Exec(context.Background(), "INSERT INTO t VALUES (1)"); err != nil {
			return err
		}
		return tx.Exec(context.Background(), "INSERT INTO t VALUES (2)")
	})
	if err != nil {
		t.Fatalf("ExecTx error: %v", err)
	}

	// Uploaded content should contain BEGIN/COMMIT wrapper
	if !strings.Contains(runner.uploadedContent, "BEGIN;") {
		t.Fatal("expected BEGIN in uploaded content")
	}
	if !strings.Contains(runner.uploadedContent, "COMMIT;") {
		t.Fatal("expected COMMIT in uploaded content")
	}
	if !strings.Contains(runner.uploadedContent, "INSERT INTO t VALUES (1)") {
		t.Fatal("expected first statement in uploaded content")
	}
}

func TestSSHExecutorShellQuotesDatabase(t *testing.T) {
	runner := &mockRunner{}
	exec := &SSHExecutor{Runner: runner, UsePeerAuth: true}
	conn := ConnParams{Host: "db", Port: 5432, User: "postgres", Database: "my-db"}

	_ = exec.Exec(context.Background(), conn, "SELECT 1")

	if !strings.Contains(runner.lastCmd, "-d 'my-db'") {
		t.Fatalf("database should be shell-quoted, got: %s", runner.lastCmd)
	}
}

func TestSSHExecutorSkipsPsqlrc(t *testing.T) {
	runner := &mockRunner{}
	exec := &SSHExecutor{Runner: runner, UsePeerAuth: true}
	conn := ConnParams{Host: "db", Port: 5432, User: "postgres", Database: "test"}

	_ = exec.Exec(context.Background(), conn, "SELECT 1")

	if !strings.Contains(runner.lastCmd, " -X ") {
		t.Fatalf("expected -X flag to skip .psqlrc, got: %s", runner.lastCmd)
	}
}

func TestSSHExecutorTCPAuth_PgpassFile(t *testing.T) {
	runner := &mockRunner{}
	exec := &SSHExecutor{Runner: runner, UsePeerAuth: false, Password: "s3cret"}
	conn := ConnParams{Host: "db", Port: 5433, User: "yugabyte", Database: "test"}

	_ = exec.Exec(context.Background(), conn, "SELECT 1")

	// Find the psql command (not the cleanup rm -f from deferred pgpass cleanup)
	var psqlCmd string
	for _, cmd := range runner.allCmds {
		if strings.Contains(cmd, "PGPASSFILE=") {
			psqlCmd = cmd
			break
		}
	}
	if psqlCmd == "" {
		t.Fatalf("expected a command with PGPASSFILE reference, got commands: %v", runner.allCmds)
	}
	if strings.Contains(psqlCmd, "s3cret") {
		t.Fatalf("password must not appear in shell command string, got: %s", psqlCmd)
	}
	if strings.Contains(psqlCmd, "PGPASSWORD") {
		t.Fatalf("should not use PGPASSWORD env var, got: %s", psqlCmd)
	}
	if strings.Contains(psqlCmd, "sudo") {
		t.Fatal("TCP auth should not use sudo")
	}
	// Pgpass file uploaded via SCP with correct content and permissions
	var pgpassUpload *fwssh.UploadOptions
	for i := range runner.allUploads {
		if strings.Contains(runner.allUploads[i].RemotePath, ".pgpass") {
			pgpassUpload = &runner.allUploads[i]
			break
		}
	}
	if pgpassUpload == nil {
		t.Fatal("expected a pgpass file upload")
	}
	if pgpassUpload.Mode != 0600 {
		t.Fatalf("pgpass file should be uploaded with mode 0600, got: %o", pgpassUpload.Mode)
	}
}

func TestSSHExecutorTCPAuth_NoPassword_WhenEmpty(t *testing.T) {
	runner := &mockRunner{}
	exec := &SSHExecutor{Runner: runner, UsePeerAuth: false}
	conn := ConnParams{Host: "db", Port: 5433, User: "yugabyte", Database: "test"}

	_ = exec.Exec(context.Background(), conn, "SELECT 1")

	if strings.Contains(runner.lastCmd, "PGPASSWORD") {
		t.Fatalf("should not set PGPASSWORD when empty, got: %s", runner.lastCmd)
	}
	if strings.Contains(runner.lastCmd, "PGPASSFILE") {
		t.Fatalf("should not create pgpass file when no password, got: %s", runner.lastCmd)
	}
}

func TestSSHExecutorPeerAuth_SkipsPsqlrc(t *testing.T) {
	runner := &mockRunner{stdout: "t\n"}
	exec := &SSHExecutor{Runner: runner, UsePeerAuth: true}
	conn := ConnParams{Host: "db", Port: 5432, User: "postgres", Database: "test"}

	var exists bool
	_ = exec.QueryRow(context.Background(), conn, "SELECT EXISTS(SELECT 1)", nil, &exists)

	if !strings.Contains(runner.lastCmd, " -X ") {
		t.Fatalf("QueryRow should pass -X, got: %s", runner.lastCmd)
	}
}
