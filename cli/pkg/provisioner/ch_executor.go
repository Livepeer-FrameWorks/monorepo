package provisioner

import (
	"context"
	"fmt"
	"os"
	"time"

	"frameworks/cli/pkg/ssh"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// CHExecutor abstracts ClickHouse SQL execution so provisioners can work
// over direct TCP (DirectCHExecutor) or piped through SSH (SSHCHExecutor).
type CHExecutor interface {
	Exec(ctx context.Context, addr string, port int, user, password, database, sqlText string) error
}

// ---------------------------------------------------------------------------
// DirectCHExecutor — wraps the ClickHouse native driver
// ---------------------------------------------------------------------------

// DirectCHExecutor implements CHExecutor using the ClickHouse Go driver.
type DirectCHExecutor struct{}

func (d *DirectCHExecutor) Exec(ctx context.Context, addr string, port int, user, password, database, sqlText string) error {
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{fmt.Sprintf("%s:%d", addr, port)},
		Auth: clickhouse.Auth{
			Database: database,
			Username: user,
			Password: password,
		},
	})
	if err != nil {
		return fmt.Errorf("connect to clickhouse: %w", err)
	}
	defer conn.Close()

	if err := conn.Exec(ctx, sqlText); err != nil {
		return fmt.Errorf("clickhouse exec: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// SSHCHExecutor — uploads SQL files and executes via clickhouse-client
// ---------------------------------------------------------------------------

// SSHCHExecutor implements CHExecutor by uploading SQL content as a file
// and executing it via clickhouse-client on the remote host.
type SSHCHExecutor struct {
	Runner     ssh.Runner
	BinaryPath string // defaults to "clickhouse-client"
}

func (s *SSHCHExecutor) binaryPath() string {
	if s.BinaryPath != "" {
		return s.BinaryPath
	}
	return "clickhouse-client"
}

func (s *SSHCHExecutor) Exec(ctx context.Context, _ string, port int, user, password, database, sqlText string) error {
	localFile, err := os.CreateTemp("", "frameworks-chsql-*.sql")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	localPath := localFile.Name()
	defer os.Remove(localPath)

	if _, writeErr := localFile.WriteString(sqlText); writeErr != nil {
		localFile.Close()
		return fmt.Errorf("write temp file: %w", writeErr)
	}
	localFile.Close()

	remotePath := fmt.Sprintf("/tmp/frameworks-chsql-%d.sql", time.Now().UnixNano())

	if uploadErr := s.Runner.Upload(ctx, ssh.UploadOptions{
		LocalPath:  localPath,
		RemotePath: remotePath,
		Mode:       0600,
	}); uploadErr != nil {
		return fmt.Errorf("upload sql file: %w", uploadErr)
	}

	envPrefix := ""
	if password != "" {
		envPrefix = fmt.Sprintf("CLICKHOUSE_PASSWORD=%s ", shellQuote(password))
	}

	cmd := fmt.Sprintf("trap 'rm -f %s' EXIT; %s%s --host localhost --port %d --user %s --database %s --queries-file %s",
		shellQuote(remotePath),
		envPrefix,
		s.binaryPath(),
		port,
		shellQuote(user),
		shellQuote(database),
		shellQuote(remotePath))

	result, err := s.Runner.Run(ctx, cmd)
	if err != nil {
		return fmt.Errorf("ssh clickhouse exec: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("clickhouse-client failed (exit %d): %s", result.ExitCode, result.Stderr)
	}
	return nil
}
