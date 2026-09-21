package cmd

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
)

func TestAuthorityRestorePhaseCannotStartBeforeEveryFenceSucceeds(t *testing.T) {
	for _, tc := range []struct{ phase, fail, want string }{
		{"prepare", "", "stop,fence"},
		{"prepare", "stop", "stop"},
		{"prepare", "fence", "stop,fence"},
		{"complete", "", "check,fence,start"},
		{"complete", "check", "check"},
		{"complete", "fence", "check,fence"},
		{"complete", "start", "check,fence,start"},
	} {
		t.Run(tc.phase+"/"+tc.fail, func(t *testing.T) {
			var calls []string
			step := func(name string) error {
				calls = append(calls, name)
				if name == tc.fail {
					return errors.New("injected failure")
				}
				return nil
			}
			err := executeAuthorityRestorePhase(tc.phase, step, func() error { return step("fence") })
			if (err != nil) != (tc.fail != "") || strings.Join(calls, ",") != tc.want {
				t.Fatalf("calls=%v err=%v, want %s", calls, err, tc.want)
			}
		})
	}
}

func TestAuthorityRestoreIncludesEveryReplicaAndAlias(t *testing.T) {
	m := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"a": {}, "b": {}, "c": {}},
		Services: map[string]inventory.ServiceConfig{
			"foghorn-us": {Enabled: true, Deploy: "foghorn", Mode: "native", Hosts: []string{"b", "a"}},
			"foghorn-eu": {Enabled: true, Deploy: "foghorn", Mode: "native", Host: "c"},
		},
	}
	replicas, err := authorityRestoreReplicas(m)
	if err != nil || len(replicas) != 3 {
		t.Fatalf("replicas=%+v err=%v", replicas, err)
	}
	delete(m.Hosts, "a")
	if _, err := authorityRestoreReplicas(m); err == nil {
		t.Fatal("missing replica host accepted")
	}
}

func TestAuthorityRestoreRequiresAStoppedLoadedUnit(t *testing.T) {
	for _, tc := range []struct {
		name, load, active, pid string
		ok                      bool
	}{
		{"stopped", "loaded", "inactive", "0", true},
		{"running", "loaded", "active", "12", false},
		{"stopping", "loaded", "deactivating", "12", false},
		{"absent", "not-found", "inactive", "0", false},
		{"lingering process", "loaded", "inactive", "12", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stub := "#!/bin/sh\ncase \"$*\" in\n*LoadState*) echo '" + tc.load + "';;\n*ActiveState*) echo '" + tc.active + "';;\n*MainPID*) echo '" + tc.pid + "';;\n*) exit 9;;\nesac\n"
			if err := os.WriteFile(filepath.Join(dir, "systemctl"), []byte(stub), 0o755); err != nil {
				t.Fatal(err)
			}
			script := authorityRestoreReplicaCommand("check")
			if !strings.Contains(script, "frameworks-foghorn.service") {
				t.Fatal("not using role-owned unit")
			}
			cmd := exec.Command("sh", "-c", script)
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
			output, err := cmd.CombinedOutput()
			if (err == nil) != tc.ok {
				t.Fatalf("err=%v output=%s", err, output)
			}
		})
	}
}
