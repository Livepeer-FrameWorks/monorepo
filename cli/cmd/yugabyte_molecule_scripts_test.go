package cmd

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"frameworks/cli/pkg/provisioner"
)

var updateYugabyteMoleculeScripts = flag.Bool("update-yugabyte-molecule-scripts", false, "rewrite the CLI scripts the yugabyte molecule scenario runs")

// yugabyteMoleculeScriptsDir holds the on-node YSQL scripts the CLI generates,
// which the yugabyte role's molecule verifier runs against a tserver bound to
// the container's own address, as the role binds a node's WireGuard address.
const yugabyteMoleculeScriptsDir = "../../ansible/collections/ansible_collections/frameworks/infra/roles/yugabyte/molecule/default/cli-scripts"

// The scripts in the molecule scenario must be the ones this CLI generates, so
// the verifier proves the CLI's own clients reach a non-loopback YSQL bind.
func TestYugabyteMoleculeRunsTheCLIsYSQLScripts(t *testing.T) {
	target := postgresSnapshotTarget{HostName: "molecule", Port: 5433, User: "yugabyte", Binary: "ysqlsh", Databases: []string{"yugabyte"}}
	scripts := map[string]string{
		"ysql-timing-probe.sh":     yugabyteYSQLTimingProbe(provisioner.ConnParams{Port: 5433, User: "yugabyte", Database: "yugabyte"}),
		"media-authority-probe.sh": mediaAuthorityDatabaseScript(target, []mediaAuthorityDatabaseProbe{{Database: "yugabyte", SQL: "SET SESSION CHARACTERISTICS AS TRANSACTION READ ONLY;\nSELECT 1;"}}),
		"post-expand-report.sh":    postExpandReportScript(target),
		"postgres-snapshot.sh":     postgresSnapshotScript(target),
	}
	checkYugabyteMoleculeScripts(t, yugabyteMoleculeScriptsDir, scripts)
}

func checkYugabyteMoleculeScripts(t *testing.T, dir string, scripts map[string]string) {
	t.Helper()
	for name, script := range scripts {
		path := filepath.Join(dir, name)
		if *updateYugabyteMoleculeScripts {
			if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v (run go test ./cli/cmd -run %s -update-yugabyte-molecule-scripts)", err, t.Name())
		}
		if string(got) != script {
			t.Errorf("%s differs from the script the CLI generates (run go test ./cli/cmd -run %s -update-yugabyte-molecule-scripts)", path, t.Name())
		}
	}
}
