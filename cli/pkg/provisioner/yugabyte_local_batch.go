package provisioner

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"frameworks/cli/pkg/ssh"
)

// YugabyteLocalQuery is one read-only statement that ysqlsh runs against Database on the tserver that answers.
type YugabyteLocalQuery struct {
	Database string
	SQL      string
}

// YugabyteLocalRows is the answer to one YugabyteLocalQuery: its rows as psql -tA columns, or why it failed.
type YugabyteLocalRows struct {
	Rows [][]string
	Err  error
}

// Scan hands every row to fn the way SSHExecutor.QueryRows does.
func (r YugabyteLocalRows) Scan(fn func(scan func(dest ...any) error) error) error {
	if r.Err != nil {
		return r.Err
	}
	for _, cols := range r.Rows {
		scan := func(dest ...any) error {
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
		if err := fn(scan); err != nil {
			return err
		}
	}
	return nil
}

const (
	yugabyteBatchQueryMarker = "@@fw-query "
	yugabyteBatchDoneMarker  = "@@fw-done "
	yugabyteBatchSQLEnd      = "FW_SQL_EOF"
)

// RunYugabyteLocalQueries runs every query through the node's ysqlsh in one SSH command, passing each statement on
// stdin through a quoted heredoc so nothing is uploaded and no SQL is shell-expanded. A failing query does not stop
// the others; its error carries ysqlsh's exit status and stderr. The returned error means the command as a whole
// failed, for example because the host was unreachable or the node has no ysqlsh.
func RunYugabyteLocalQueries(ctx context.Context, runner ssh.Runner, port int, user string, queries []YugabyteLocalQuery) ([]YugabyteLocalRows, error) {
	script, err := yugabyteLocalQueriesScript(port, user, queries)
	if err != nil {
		return nil, err
	}
	result, err := runner.Run(ctx, script)
	if err != nil {
		return nil, err
	}
	return parseYugabyteLocalQueries(result.Stdout, queries), nil
}

func yugabyteLocalQueriesScript(port int, user string, queries []YugabyteLocalQuery) (string, error) {
	var b strings.Builder
	b.WriteString("fw_err=\"$(mktemp)\" || exit 1\ntrap 'rm -f \"$fw_err\"' EXIT\n")
	b.WriteString(YugabyteBinaryResolverShell)
	b.WriteString("\nyb_client=\"$(fw_yb_bin ysqlsh)\" || exit 1\n")
	fmt.Fprintf(&b, `fw_query() {
  printf '%%s%%s\n' '%s' "$1"
  "$yb_client" -X -h localhost -p %d -U %s -d "$2" -v ON_ERROR_STOP=1 -tA -f - 2>"$fw_err"
  fw_rc=$?
  printf '\n%%s%%s %%s ' '%s' "$1" "$fw_rc"
  tr '\n' ' ' <"$fw_err"
  printf '\n'
}
`, yugabyteBatchQueryMarker, port, shellQuote(user), yugabyteBatchDoneMarker)
	for i, query := range queries {
		for line := range strings.SplitSeq(query.SQL, "\n") {
			if strings.TrimSpace(line) == yugabyteBatchSQLEnd {
				return "", fmt.Errorf("query %d for %s contains the heredoc terminator %s", i, query.Database, yugabyteBatchSQLEnd)
			}
		}
		fmt.Fprintf(&b, "fw_query %d %s <<'%s'\n%s\n%s\n", i, shellQuote(query.Database), yugabyteBatchSQLEnd, query.SQL, yugabyteBatchSQLEnd)
	}
	b.WriteString("exit 0\n")
	return b.String(), nil
}

func parseYugabyteLocalQueries(stdout string, queries []YugabyteLocalQuery) []YugabyteLocalRows {
	results := make([]YugabyteLocalRows, len(queries))
	done := make([]bool, len(queries))
	current := -1
	for line := range strings.SplitSeq(stdout, "\n") {
		switch {
		case strings.HasPrefix(line, yugabyteBatchQueryMarker):
			current = -1
			if i, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, yugabyteBatchQueryMarker))); err == nil && i >= 0 && i < len(queries) {
				current = i
			}
		case strings.HasPrefix(line, yugabyteBatchDoneMarker):
			fields := strings.SplitN(strings.TrimPrefix(line, yugabyteBatchDoneMarker), " ", 3)
			current = -1
			if len(fields) < 2 {
				continue
			}
			i, err := strconv.Atoi(fields[0])
			if err != nil || i < 0 || i >= len(queries) {
				continue
			}
			done[i] = true
			if fields[1] != "0" {
				stderr := ""
				if len(fields) == 3 {
					stderr = strings.TrimSpace(fields[2])
				}
				results[i] = YugabyteLocalRows{Err: fmt.Errorf("ysqlsh -d %s exited %s: %s", queries[i].Database, fields[1], stderr)}
			}
		case current >= 0 && line != "":
			results[current].Rows = append(results[current].Rows, strings.Split(line, "|"))
		}
	}
	for i := range results {
		if !done[i] {
			results[i] = YugabyteLocalRows{Err: fmt.Errorf("ysqlsh -d %s returned no result", queries[i].Database)}
		}
	}
	return results
}
