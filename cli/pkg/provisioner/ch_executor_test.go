package provisioner

import (
	"context"
	"strings"
	"testing"
)

func TestSSHCHExecutorExec_UploadBased(t *testing.T) {
	runner := &mockRunner{}
	exec := &SSHCHExecutor{Runner: runner}

	err := exec.Exec(context.Background(), "ch-host", 9000, "default", "", "periscope", "CREATE TABLE t (id UInt32) ENGINE = Memory")
	if err != nil {
		t.Fatalf("Exec error: %v", err)
	}

	// SQL should be in the uploaded file, not the command
	if runner.uploadedContent != "CREATE TABLE t (id UInt32) ENGINE = Memory" {
		t.Fatalf("uploaded content = %q, want SQL", runner.uploadedContent)
	}
	if strings.Contains(runner.lastCmd, "CREATE TABLE") {
		t.Fatal("SQL content must not appear in shell command")
	}

	if !strings.Contains(runner.lastCmd, "clickhouse-client") {
		t.Fatalf("expected clickhouse-client in command, got: %s", runner.lastCmd)
	}
	if !strings.Contains(runner.lastCmd, "--database 'periscope'") {
		t.Fatalf("expected quoted --database, got: %s", runner.lastCmd)
	}
	if !strings.Contains(runner.lastCmd, "--queries-file") {
		t.Fatalf("expected --queries-file, got: %s", runner.lastCmd)
	}
	if !strings.Contains(runner.lastCmd, "trap 'rm -f") {
		t.Fatal("expected trap for cleanup")
	}
}

func TestSSHCHExecutorExec_WithPassword(t *testing.T) {
	runner := &mockRunner{}
	exec := &SSHCHExecutor{Runner: runner}

	err := exec.Exec(context.Background(), "ch-host", 9000, "frameworks", "s3cret", "periscope", "SELECT 1")
	if err != nil {
		t.Fatalf("Exec error: %v", err)
	}

	if !strings.Contains(runner.lastCmd, "CLICKHOUSE_PASSWORD='s3cret'") {
		t.Fatalf("expected CLICKHOUSE_PASSWORD env var, got: %s", runner.lastCmd)
	}
	if strings.Contains(runner.lastCmd, "--password") {
		t.Fatal("password should not be in argv (process list leak)")
	}
	if !strings.Contains(runner.lastCmd, "--user 'frameworks'") {
		t.Fatalf("expected quoted --user, got: %s", runner.lastCmd)
	}
}

func TestSSHCHExecutorExec_NoPassword(t *testing.T) {
	runner := &mockRunner{}
	exec := &SSHCHExecutor{Runner: runner}

	_ = exec.Exec(context.Background(), "ch-host", 9000, "default", "", "default", "SELECT 1")

	if strings.Contains(runner.lastCmd, "--password") {
		t.Fatalf("should not include --password when empty, got: %s", runner.lastCmd)
	}
}

func TestSSHCHExecutorExec_CustomBinary(t *testing.T) {
	runner := &mockRunner{}
	exec := &SSHCHExecutor{Runner: runner, BinaryPath: "/opt/ch/bin/clickhouse-client"}

	_ = exec.Exec(context.Background(), "ch-host", 9000, "default", "", "default", "SELECT 1")

	if !strings.Contains(runner.lastCmd, "/opt/ch/bin/clickhouse-client") {
		t.Fatalf("expected custom binary path, got: %s", runner.lastCmd)
	}
}

func TestSSHCHExecutorExec_Error(t *testing.T) {
	runner := &mockRunner{stderr: "table already exists", exitCode: 1}
	exec := &SSHCHExecutor{Runner: runner}

	err := exec.Exec(context.Background(), "ch-host", 9000, "default", "", "default", "CREATE TABLE t ...")
	if err == nil {
		t.Fatal("expected error for non-zero exit code")
	}
	if !strings.Contains(err.Error(), "table already exists") {
		t.Fatalf("expected stderr in error, got: %v", err)
	}
}

func TestSSHCHExecutorExec_SQLNeverInCommand(t *testing.T) {
	runner := &mockRunner{}
	exec := &SSHCHExecutor{Runner: runner}
	malicious := "'; system('rm -rf /'); --"

	_ = exec.Exec(context.Background(), "ch-host", 9000, "default", "", "default", malicious)

	if strings.Contains(runner.lastCmd, "rm -rf") {
		t.Fatal("SQL content must not appear in shell command")
	}
	if runner.uploadedContent != malicious {
		t.Fatal("SQL should be in uploaded file")
	}
}
