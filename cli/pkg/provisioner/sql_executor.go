package provisioner

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/pkg/ssh"

	"github.com/lib/pq"
)

// ConnParams holds the parameters needed to reach a Postgres-compatible database.
type ConnParams struct {
	Host     string
	Port     int
	User     string
	Database string
	SSLMode  string // defaults to "disable"
}

// ConnStr returns a lib/pq-style connection string.
func (c ConnParams) ConnStr() string {
	sslMode := c.SSLMode
	if sslMode == "" {
		sslMode = "disable"
	}
	return fmt.Sprintf("host=%s port=%d user=%s dbname=%s sslmode=%s",
		c.Host, c.Port, c.User, c.Database, sslMode)
}

// SQLExecutor abstracts Postgres SQL execution so provisioners can work
// over direct TCP (DirectExecutor) or piped through SSH (SSHExecutor).
type SQLExecutor interface {
	Exec(ctx context.Context, conn ConnParams, sqlText string) error
	QueryRow(ctx context.Context, conn ConnParams, query string, args []any, dest ...any) error
	QueryRows(ctx context.Context, conn ConnParams, query string, args []any, scanFn func(scan func(dest ...any) error) error) error
	ExecTx(ctx context.Context, conn ConnParams, fn func(TxExecutor) error) error
}

// TxExecutor runs statements within an active transaction.
type TxExecutor interface {
	Exec(ctx context.Context, sqlText string) error
}

// ---------------------------------------------------------------------------
// DirectExecutor — wraps database/sql for direct TCP connections
// ---------------------------------------------------------------------------

// DirectExecutor implements SQLExecutor using database/sql (lib/pq).
type DirectExecutor struct{}

func (d *DirectExecutor) Exec(ctx context.Context, conn ConnParams, sqlText string) error {
	db, err := sql.Open("postgres", conn.ConnStr())
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer db.Close()

	if _, err := db.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("exec: %w", err)
	}
	return nil
}

func (d *DirectExecutor) QueryRow(ctx context.Context, conn ConnParams, query string, args []any, dest ...any) error {
	db, err := sql.Open("postgres", conn.ConnStr())
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer db.Close()

	return db.QueryRowContext(ctx, query, args...).Scan(dest...)
}

func (d *DirectExecutor) QueryRows(ctx context.Context, conn ConnParams, query string, args []any, scanFn func(scan func(dest ...any) error) error) error {
	db, err := sql.Open("postgres", conn.ConnStr())
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("query: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		if err := scanFn(rows.Scan); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (d *DirectExecutor) ExecTx(ctx context.Context, conn ConnParams, fn func(TxExecutor) error) error {
	db, err := sql.Open("postgres", conn.ConnStr())
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	if err := fn(&directTxExecutor{tx: tx}); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("%w (rollback also failed: %w)", err, rbErr)
		}
		return err
	}
	return tx.Commit()
}

type directTxExecutor struct {
	tx *sql.Tx
}

func (t *directTxExecutor) Exec(ctx context.Context, sqlText string) error {
	if _, err := t.tx.ExecContext(ctx, sqlText); err != nil {
		return fmt.Errorf("tx exec: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// SSHExecutor — uploads SQL files and executes via psql on the remote host
// ---------------------------------------------------------------------------

// SSHExecutor implements SQLExecutor by uploading SQL content as a file
// and executing it via psql (or ysqlsh) on the remote host.
//
// Auth strategy:
//   - UsePeerAuth=true (Postgres): sudo -u <user> psql via Unix socket (peer auth)
//   - UsePeerAuth=false (YugabyteDB): ysqlsh -h localhost via TCP with PGPASSWORD
type SSHExecutor struct {
	Runner      ssh.Runner
	BinaryPath  string // defaults to "psql"
	UsePeerAuth bool   // true = sudo + Unix socket; false = TCP + -h localhost
	Password    string // for TCP auth mode; passed via PGPASSWORD env var
}

func (s *SSHExecutor) binaryPath() string {
	if s.BinaryPath != "" {
		return s.BinaryPath
	}
	return "psql"
}

// uploadSQL writes sqlContent to a local temp file, uploads it to the remote
// host via SCP, and returns the remote path plus a cleanup function for the
// local file. The remote file is cleaned up by a trap in the shell command.
func (s *SSHExecutor) uploadSQL(ctx context.Context, sqlContent string) (remotePath string, cleanup func(), err error) {
	localFile, err := os.CreateTemp("", "frameworks-sql-*.sql")
	if err != nil {
		return "", nil, fmt.Errorf("create temp file: %w", err)
	}
	localPath := localFile.Name()
	cleanup = func() { os.Remove(localPath) }

	if _, err := localFile.WriteString(sqlContent); err != nil {
		localFile.Close()
		cleanup()
		return "", nil, fmt.Errorf("write temp file: %w", err)
	}
	localFile.Close()

	remotePath = fmt.Sprintf("/tmp/frameworks-sql-%d.sql", time.Now().UnixNano())

	if err := s.Runner.Upload(ctx, ssh.UploadOptions{
		LocalPath:  localPath,
		RemotePath: remotePath,
		Mode:       0600,
	}); err != nil {
		cleanup()
		return "", nil, fmt.Errorf("upload sql file: %w", err)
	}

	return remotePath, cleanup, nil
}

// buildCommand constructs the full shell command with trap-based cleanup,
// correct auth mode, and optional extra psql flags (e.g. "-tA").
// Always passes -X to skip user startup files (.psqlrc / .ysqlshrc).
func (s *SSHExecutor) buildCommand(conn ConnParams, remotePath string, extraFlags ...string) string {
	extra := ""
	if len(extraFlags) > 0 {
		extra = " " + strings.Join(extraFlags, " ")
	}

	trap := fmt.Sprintf("trap 'rm -f %s' EXIT", shellQuote(remotePath))

	if s.UsePeerAuth {
		return fmt.Sprintf("%s; sudo -u %s %s -X -p %d -d %s -v ON_ERROR_STOP=1%s -f %s",
			trap,
			shellQuote(conn.User),
			s.binaryPath(),
			conn.Port,
			shellQuote(conn.Database),
			extra,
			shellQuote(remotePath))
	}

	pgpassword := ""
	if s.Password != "" {
		pgpassword = fmt.Sprintf("PGPASSWORD=%s ", shellQuote(s.Password))
	}

	return fmt.Sprintf("%s; %s%s -X -h localhost -p %d -U %s -d %s -v ON_ERROR_STOP=1%s -f %s",
		trap,
		pgpassword,
		s.binaryPath(),
		conn.Port,
		shellQuote(conn.User),
		shellQuote(conn.Database),
		extra,
		shellQuote(remotePath))
}

func (s *SSHExecutor) Exec(ctx context.Context, conn ConnParams, sqlText string) error {
	remotePath, cleanup, err := s.uploadSQL(ctx, sqlText)
	if err != nil {
		return err
	}
	defer cleanup()

	cmd := s.buildCommand(conn, remotePath)
	result, err := s.Runner.Run(ctx, cmd)
	if err != nil {
		return fmt.Errorf("ssh exec: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("psql failed (exit %d): %s", result.ExitCode, result.Stderr)
	}
	return nil
}

func (s *SSHExecutor) QueryRow(ctx context.Context, conn ConnParams, query string, args []any, dest ...any) error {
	interpolated, err := interpolateArgs(query, args)
	if err != nil {
		return err
	}

	remotePath, cleanup, err := s.uploadSQL(ctx, interpolated)
	if err != nil {
		return err
	}
	defer cleanup()

	cmd := s.buildCommand(conn, remotePath, "-tA")
	result, err := s.Runner.Run(ctx, cmd)
	if err != nil {
		return fmt.Errorf("ssh query: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("psql query failed (exit %d): %s", result.ExitCode, result.Stderr)
	}

	output := strings.TrimRight(result.Stdout, "\n")
	if output == "" {
		return sql.ErrNoRows
	}

	cols := strings.Split(output, "|")
	if len(cols) < len(dest) {
		return fmt.Errorf("expected %d columns, got %d", len(dest), len(cols))
	}

	for i, d := range dest {
		if err := scanPsqlValue(cols[i], d); err != nil {
			return fmt.Errorf("scan column %d: %w", i, err)
		}
	}
	return nil
}

func (s *SSHExecutor) QueryRows(ctx context.Context, conn ConnParams, query string, args []any, scanFn func(scan func(dest ...any) error) error) error {
	interpolated, err := interpolateArgs(query, args)
	if err != nil {
		return err
	}

	remotePath, cleanup, err := s.uploadSQL(ctx, interpolated)
	if err != nil {
		return err
	}
	defer cleanup()

	cmd := s.buildCommand(conn, remotePath, "-tA")
	result, err := s.Runner.Run(ctx, cmd)
	if err != nil {
		return fmt.Errorf("ssh query rows: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("psql query failed (exit %d): %s", result.ExitCode, result.Stderr)
	}

	output := strings.TrimRight(result.Stdout, "\n")
	if output == "" {
		return nil
	}

	for line := range strings.SplitSeq(output, "\n") {
		cols := strings.Split(line, "|")
		scanFunc := func(dest ...any) error {
			if len(cols) < len(dest) {
				return fmt.Errorf("expected %d columns, got %d", len(dest), len(cols))
			}
			for i, d := range dest {
				if err := scanPsqlValue(cols[i], d); err != nil {
					return fmt.Errorf("scan column %d: %w", i, err)
				}
			}
			return nil
		}
		if err := scanFn(scanFunc); err != nil {
			return err
		}
	}
	return nil
}

func (s *SSHExecutor) ExecTx(ctx context.Context, conn ConnParams, fn func(TxExecutor) error) error {
	txe := &sshTxExecutor{}
	if err := fn(txe); err != nil {
		return err
	}

	script := "BEGIN;\n" + strings.Join(txe.stmts, "\n") + "\nCOMMIT;\n"
	return s.Exec(ctx, conn, script)
}

type sshTxExecutor struct {
	stmts []string
}

func (t *sshTxExecutor) Exec(_ context.Context, sqlText string) error {
	t.stmts = append(t.stmts, sqlText)
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

var paramRe = regexp.MustCompile(`\$(\d+)`)

// interpolateArgs replaces $1, $2, ... with safely-quoted literal values,
// for use with psql which doesn't support parameterized queries.
func interpolateArgs(query string, args []any) (string, error) {
	if len(args) == 0 {
		return query, nil
	}

	var replaceErr error
	result := paramRe.ReplaceAllStringFunc(query, func(match string) string {
		idx, err := strconv.Atoi(match[1:])
		if err != nil || idx < 1 || idx > len(args) {
			replaceErr = fmt.Errorf("invalid parameter %s (have %d args)", match, len(args))
			return match
		}
		return quoteLiteral(args[idx-1])
	})
	return result, replaceErr
}

func quoteLiteral(v any) string {
	switch val := v.(type) {
	case string:
		return pq.QuoteLiteral(val)
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case bool:
		if val {
			return "TRUE"
		}
		return "FALSE"
	default:
		return pq.QuoteLiteral(fmt.Sprintf("%v", val))
	}
}

func scanPsqlValue(raw string, dest any) error {
	switch d := dest.(type) {
	case *string:
		*d = raw
		return nil
	case *bool:
		switch raw {
		case "t", "true", "TRUE":
			*d = true
		case "f", "false", "FALSE":
			*d = false
		default:
			return fmt.Errorf("cannot parse %q as bool", raw)
		}
		return nil
	case *int:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return fmt.Errorf("cannot parse %q as int: %w", raw, err)
		}
		*d = n
		return nil
	case *int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("cannot parse %q as int64: %w", raw, err)
		}
		*d = n
		return nil
	default:
		return fmt.Errorf("unsupported scan type %T", dest)
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
