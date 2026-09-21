package provisioner

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/ansiblerun"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// The role provisioner implements the full Provisioner contract, including Deploy (install/configure/start without
// validate) so cluster upgrade can own the readiness gate + rollback (a readiness failure inside Provision would
// bypass the rollback block).
var _ Provisioner = (*RolePlaybookProvisioner)(nil)
var _ Stopper = (*RolePlaybookProvisioner)(nil)

func TestRoleStopSelectsOnlyStopAndPropagatesFailure(t *testing.T) {
	root := t.TempDir()
	requirements := filepath.Join(root, "requirements.yml")
	if err := os.WriteFile(requirements, []byte("collections: []\nroles: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	galaxy := filepath.Join(root, "galaxy")
	if err := os.WriteFile(galaxy, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(root, "playbook")
	pool := ssh.NewPool(time.Second, "")
	defer pool.Close()
	prov := &RolePlaybookProvisioner{
		BaseProvisioner: NewBaseProvisioner("purser", pool),
		RoleName:        "go_service", PlaybookRel: "go_service.yml", AnsibleRoot: root,
		Builder: func(context.Context, inventory.Host, ServiceConfig, RoleBuildHelpers) (map[string]any, error) {
			return map[string]any{}, nil
		},
		Executor: &ansiblerun.Executor{Binary: binary},
		Ensurer:  &ansiblerun.CollectionEnsurer{RequirementsFile: requirements, CacheDir: filepath.Join(root, "cache"), Binary: galaxy},
	}
	for _, fail := range []bool{false, true} {
		script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > args\n"
		if fail {
			script += "exit 7\n"
		}
		if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
		err := prov.Stop(context.Background(), inventory.Host{Name: "local", ExternalIP: "127.0.0.1"}, ServiceConfig{})
		if (err != nil) != fail {
			t.Fatalf("Stop error = %v, want failure %v", err, fail)
		}
		args, readErr := os.ReadFile(filepath.Join(root, "args"))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.Contains(string(args), "--tags=stop\n") || strings.Contains(string(args), "cleanup") {
			t.Fatalf("Stop selected wrong tasks: %s", args)
		}
	}
}

func TestAnsibleCollectionsPathPrefersRepoCollection(t *testing.T) {
	root := filepath.Join("repo", "ansible")
	cache := filepath.Join("cache", "collections")

	got := ansibleCollectionsPath(root, cache)
	want := filepath.Join(root, "collections") + string(os.PathListSeparator) + cache
	if got != want {
		t.Fatalf("ansibleCollectionsPath = %q, want %q", got, want)
	}
}

func TestAnsibleCollectionsPathAvoidsDuplicateLocalPath(t *testing.T) {
	root := filepath.Join("repo", "ansible")
	local := filepath.Join(root, "collections")

	if got := ansibleCollectionsPath(root, local); got != local {
		t.Fatalf("ansibleCollectionsPath duplicate local = %q, want %q", got, local)
	}
}
