package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotCollectsCanonicalLedgersAndSkipsAbsentSupportLedgers(t *testing.T) {
	for _, database := range []string{"platform", "support", "denied"} {
		t.Run(database, func(t *testing.T) {
			dir := t.TempDir()
			client := filepath.Join(dir, "snapshot-psql")
			// The fake exercises the generated shell, including relation discovery
			// and error handling, without reading tenant data or opening a database.
			body := `#!/bin/sh
db=
sql=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -d) db="$2"; shift ;;
    -c) sql="$2"; shift ;;
  esac
  shift
done
case "$sql" in
  *pg_catalog.pg_class*)
    [ "$db" != denied ] || { echo 'permission denied for relation discovery'; exit 1; }
    [ "$db" != support ] || exit 0
    case "$sql" in
      *"'_data_migrations'"*) echo 'commodore._data_migrations' ;;
      *"'_migrations'"*) echo 'commodore._migrations' ;;
      *) echo 'unexpected ledger discovery'; exit 1 ;;
    esac ;;
  *'FROM commodore._data_migrations'*) echo 'required_data_migration|completed' ;;
  *'FROM commodore._migrations'*) echo 'v0.3.15|postdeploy|1|checksum' ;;
  *'FROM data_migrations'*|*'FROM _migrations'*) echo 'ERROR: relation does not exist'; exit 1 ;;
  *) echo 'metadata' ;;
esac
`
			if err := os.WriteFile(client, []byte(body), 0o700); err != nil {
				t.Fatal(err)
			}
			script := postgresSnapshotScript(postgresSnapshotTarget{Binary: client, User: "fixture", Port: 5432, Databases: []string{database}})
			cmd := exec.CommandContext(t.Context(), "bash", "-c", script)
			output, err := cmd.CombinedOutput()
			if database == "denied" {
				if err == nil || !strings.Contains(string(output), "permission denied") {
					t.Fatalf("unreadable ledger discovery was reported successful: err=%v output=%s", err, output)
				}
				return
			}
			if err != nil || strings.Contains(string(output), "ERROR:") {
				t.Fatalf("snapshot ledger collection failed: %v\n%s", err, output)
			}
			if database == "platform" && !strings.Contains(string(output), "required_data_migration|completed") {
				t.Fatalf("canonical schema-qualified data migration was not collected:\n%s", output)
			}
			if database == "support" && !strings.Contains(string(output), "not present") {
				t.Fatalf("absent support ledgers were not identified:\n%s", output)
			}
		})
	}
}
