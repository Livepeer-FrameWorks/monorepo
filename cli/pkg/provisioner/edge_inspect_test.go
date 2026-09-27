package provisioner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifyEdgeChanges(t *testing.T) {
	if got := ClassifyEdgeChanges(nil); got.Changed || got.Interrupting {
		t.Fatalf("no changes = %+v, want in sync", got)
	}
	// A MistServer binary change alone takes the rolling reload path.
	reload := ClassifyEdgeChanges([]string{"Report MistServer artifact drift in check mode", "Write edge config marker"})
	if !reload.Changed || reload.Interrupting {
		t.Fatalf("binary-only drift = %+v, want changed and not interrupting", reload)
	}
	for _, report := range EdgeInterruptingReports() {
		got := ClassifyEdgeChanges([]string{"Render env file", report})
		if !got.Changed || !got.Interrupting {
			t.Fatalf("%q = %+v, want interrupting", report, got)
		}
	}
}

// The precheck recognises an interrupting plan by handler name, so every name
// must exist as a check-mode handler in the roles the edge playbook runs.
func TestEdgeInterruptingReportsExistInRoles(t *testing.T) {
	root, err := FindAnsibleRoot()
	if err != nil {
		t.Skipf("ansible root not found: %v", err)
	}
	var handlers strings.Builder
	for _, role := range []string{"edge", "mistserver"} {
		raw, err := os.ReadFile(filepath.Join(root, "collections/ansible_collections/frameworks/infra/roles", role, "handlers/main.yml"))
		if err != nil {
			t.Fatalf("read %s handlers: %v", role, err)
		}
		handlers.Write(raw)
	}
	for _, report := range EdgeInterruptingReports() {
		if !strings.Contains(handlers.String(), "- name: "+report+"\n") {
			t.Fatalf("handler %q not found in the edge or mistserver role handlers", report)
		}
	}
}
