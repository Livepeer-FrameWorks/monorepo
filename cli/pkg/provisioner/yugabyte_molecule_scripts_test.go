package provisioner

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateYugabyteMoleculeScripts = flag.Bool("update-yugabyte-molecule-scripts", false, "rewrite the CLI scripts the yugabyte molecule scenario runs")

// The yugabyte role's molecule verifier runs this script against a tserver
// bound to the container's own address, as the role binds a node's WireGuard
// address, so it must be the script the CLI generates.
func TestYugabyteMoleculeRunsTheLocalQueriesScript(t *testing.T) {
	script, err := yugabyteLocalQueriesScript(5433, "yugabyte", []YugabyteLocalQuery{{Database: "yugabyte", SQL: "SELECT 1;"}})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("../../../ansible/collections/ansible_collections/frameworks/infra/roles/yugabyte/molecule/default/cli-scripts", "local-queries.sh")
	if *updateYugabyteMoleculeScripts {
		if writeErr := os.WriteFile(path, []byte(script), 0o644); writeErr != nil {
			t.Fatal(writeErr)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./cli/pkg/provisioner -run %s -update-yugabyte-molecule-scripts)", err, t.Name())
	}
	if string(got) != script {
		t.Errorf("%s differs from the script the CLI generates (run go test ./cli/pkg/provisioner -run %s -update-yugabyte-molecule-scripts)", path, t.Name())
	}
}
