package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The yugabyte role binds YSQL to the node's WireGuard address only. Every
// on-node script that logs in with a password must reach that listener, which
// fw_ysql_host reads from ss; loopback is refused. Each script runs here with
// a stub ss reporting that bind and a stub ysqlsh recording its arguments.
func TestOnNodeYSQLScriptsConnectToTheBoundListener(t *testing.T) {
	target := postgresSnapshotTarget{Port: 5433, User: "yugabyte", Password: "secret", Binary: "ysqlsh"}
	scripts := map[string]string{
		"media authority probe": mediaAuthorityDatabaseScript(target, []mediaAuthorityDatabaseProbe{{Database: "commodore", SQL: "SELECT 1;"}}),
		"post-expand report":    postExpandReportScript(target),
	}
	for name, script := range scripts {
		t.Run(name, func(t *testing.T) {
			bin := t.TempDir()
			args := filepath.Join(bin, "args")
			stubs := map[string]string{
				"ysqlsh": "#!/bin/sh\necho \"$*\" >>\"" + args + "\"\ncat >/dev/null\n",
				"ss":     "#!/bin/sh\necho 'LISTEN 0      4096   10.89.6.127:5433   0.0.0.0:*'\n",
			}
			for stub, body := range stubs {
				if err := os.WriteFile(filepath.Join(bin, stub), []byte(body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.CommandContext(t.Context(), "sh", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			out, _ := cmd.CombinedOutput()
			recorded, err := os.ReadFile(args)
			if err != nil {
				t.Fatalf("ysqlsh never ran: %v\n%s", err, out)
			}
			for line := range strings.SplitSeq(strings.TrimSpace(string(recorded)), "\n") {
				if !strings.Contains(line, "-h 10.89.6.127 -p 5433") {
					t.Errorf("ysqlsh connected with %q; want the WireGuard listener 10.89.6.127:5433", line)
				}
			}
		})
	}
}
