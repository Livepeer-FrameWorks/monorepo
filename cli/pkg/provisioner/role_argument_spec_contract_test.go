package provisioner

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestRoleArgumentSpecsAreValidForAnsible applies Ansible's own argument-spec rule to every role in the collection:
// an option cannot be both required and defaulted. Ansible rejects such a spec before running any task, so a single
// bad entry stops every run of the role.
func TestRoleArgumentSpecsAreValidForAnsible(t *testing.T) {
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	roles := filepath.Join(filepath.Dir(current), "..", "..", "..", "ansible", "collections", "ansible_collections", "frameworks", "infra", "roles")
	specs, err := filepath.Glob(filepath.Join(roles, "*", "meta", "main.yml"))
	if err != nil || len(specs) == 0 {
		t.Fatalf("find role argument specs: %v (%d files)", err, len(specs))
	}
	checked := 0
	for _, path := range specs {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		var meta struct {
			ArgumentSpecs map[string]struct {
				Options map[string]any `yaml:"options"`
			} `yaml:"argument_specs"`
		}
		if err = yaml.Unmarshal(data, &meta); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for entry, spec := range meta.ArgumentSpecs {
			checked += checkArgumentOptions(t, path+" "+entry, spec.Options)
		}
	}
	if checked == 0 {
		t.Fatal("no role declares argument spec options; the check would be vacuous")
	}
}

func checkArgumentOptions(t *testing.T, where string, options map[string]any) int {
	t.Helper()
	checked := 0
	for name, raw := range options {
		option, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		checked++
		_, hasDefault := option["default"]
		if required, _ := option["required"].(bool); required && hasDefault {
			t.Errorf("%s: option %s is required and has a default, which Ansible rejects", where, name)
		}
		if nested, ok := option["options"].(map[string]any); ok {
			checked += checkArgumentOptions(t, where+"."+name, nested)
		}
	}
	return checked
}
