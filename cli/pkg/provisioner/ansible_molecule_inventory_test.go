package provisioner

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
)

// node_tuning runs in its own cross-version CI job on several Ubuntu images,
// so it is in neither role list.
var crossVersionMoleculeRoles = []string{"node_tuning"}

// Every role with a molecule default scenario runs in CI and in
// make ansible-molecule-all, and neither list names a role without one.
func TestAnsibleMoleculeRoleListsMatchScenarios(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve source path")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
	scenarios, err := filepath.Glob(filepath.Join(root, "ansible/collections/ansible_collections/frameworks/infra/roles/*/molecule/default/molecule.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var want []string
	for _, scenario := range scenarios {
		role := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(scenario))))
		if !slices.Contains(crossVersionMoleculeRoles, role) {
			want = append(want, role)
		}
	}
	slices.Sort(want)
	if len(want) == 0 {
		t.Fatal("found no molecule scenarios")
	}

	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	line := regexp.MustCompile(`(?m)^ANSIBLE_MOLECULE_ROLES := (.*)$`).FindSubmatch(makefile)
	if line == nil {
		t.Fatal("Makefile defines no ANSIBLE_MOLECULE_ROLES")
	}
	makeRoles := strings.Fields(string(line[1]))
	slices.Sort(makeRoles)
	if !slices.Equal(makeRoles, want) {
		t.Errorf("Makefile ANSIBLE_MOLECULE_ROLES = %v, want the roles with a molecule default scenario %v", makeRoles, want)
	}

	workflow, err := os.ReadFile(filepath.Join(root, ".github/workflows/ansible.yml"))
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?m)^  molecule:\n(?:.*\n)*?        role:\n((?:          - [^\n]*\n)+)`).FindSubmatch(workflow)
	if block == nil {
		t.Fatal("ansible.yml has no molecule job role matrix")
	}
	var ciRoles []string
	for _, item := range strings.Split(strings.TrimSpace(string(block[1])), "\n") {
		ciRoles = append(ciRoles, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(item), "- ")))
	}
	slices.Sort(ciRoles)
	if !slices.Equal(ciRoles, want) {
		t.Errorf("ansible.yml molecule matrix = %v, want the roles with a molecule default scenario %v", ciRoles, want)
	}
}
