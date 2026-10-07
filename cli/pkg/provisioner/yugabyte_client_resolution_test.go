package provisioner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frameworks/cli/pkg/backup"
)

// Once a node selects a release under /opt/yugabyte/current, the in-place tree
// at /opt/yugabyte/{bin,postgres/bin} is no longer maintained (and is absent on
// nodes installed in the release layout). Every YugabyteDB client the CLI runs
// on a node resolves through YugabyteBinaryResolverShell, which is the only
// place allowed to name the in-place paths.
func TestYugabyteClientsResolveFromTheSelectedRelease(t *testing.T) {
	legacy := []string{"/opt/yugabyte/bin", "/opt/yugabyte/postgres/bin"}
	allowed := map[string]bool{
		"pkg/provisioner/yugabyte_relayout.go": true, // defines YugabyteBinaryResolverShell
		"pkg/detect/detector.go":               true, // detection probes current first, then an unconverted install
	}
	root := filepath.Join("..", "..")
	checked := 0
	for _, dir := range []string{"cmd", "pkg/provisioner"} {
		paths, err := filepath.Glob(filepath.Join(root, dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range paths {
			rel := filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator)))
			if strings.HasSuffix(rel, "_test.go") || allowed[rel] {
				continue
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			checked++
			for n, line := range strings.Split(string(data), "\n") {
				for _, path := range legacy {
					if strings.Contains(line, path) {
						t.Errorf("%s:%d names the in-place YugabyteDB path %s; resolve the client with fw_yb_bin:\n%s", rel, n+1, path, line)
					}
				}
			}
		}
	}
	if checked < 50 {
		t.Fatalf("scanned only %d source files", checked)
	}

	server := DatabaseServer{Engine: backup.EngineYugabyte, Port: 5433}
	command, err := DumpDatabaseSectionCommand(server, "commodore", "pre-data")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(command, "/opt/yugabyte/current") {
		t.Fatalf("backup dump does not resolve ysql_dump from the selected release:\n%s", command)
	}
}
