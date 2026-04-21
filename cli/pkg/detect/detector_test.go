package detect

import (
	"context"
	"testing"

	"frameworks/cli/pkg/inventory"
)

// fakeRunner records calls and returns scripted responses per command prefix.
type fakeRunner struct {
	responses []fakeResponse
	calls     []string
}

type fakeResponse struct {
	matchPrefix string
	exitCode    int
	stdout      string
	stderr      string
}

func (f *fakeRunner) runSSH(_ context.Context, cmd string) (int, string, string) {
	f.calls = append(f.calls, cmd)
	for _, r := range f.responses {
		if r.matchPrefix == "" || startsWith(cmd, r.matchPrefix) {
			return r.exitCode, r.stdout, r.stderr
		}
	}
	return -1, "", "no response configured"
}

func startsWith(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func newDetectorWithRunner(host inventory.Host, r sshRunner) *Detector {
	return &Detector{host: host, runner: r}
}

// TestDetect_InventoryMissServiceFoundInDocker verifies that a non-zero exit
// on the inventory check does not abort the chain — the detector must fall
// through to the docker probe.
func TestDetect_InventoryMissServiceFoundInDocker(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{matchPrefix: "cat /etc/frameworks/inventory.json", exitCode: 1, stderr: "No such file"},
			{matchPrefix: "docker ps -a", exitCode: 0, stdout: "frameworks-foghorn|running|foghorn:v1"},
			{matchPrefix: "docker inspect", exitCode: 0, stdout: "true"},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "foghorn")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !state.Exists {
		t.Fatalf("expected Exists=true after docker probe succeeded")
	}
	if state.Mode != "docker" || !state.Running {
		t.Fatalf("got mode=%q running=%v, want docker/true", state.Mode, state.Running)
	}
	if state.Version != "v1" {
		t.Fatalf("version=%q, want v1", state.Version)
	}
}

// TestDetect_AllMethodsFailReturnsNotFound verifies that exhaustively failing
// probes yield Exists=false rather than bubbling up an error.
func TestDetect_AllMethodsFailReturnsNotFound(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{matchPrefix: "", exitCode: 1, stderr: "fail"},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "some-service")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if state.Exists {
		t.Fatalf("expected Exists=false when every probe returned non-zero")
	}
}

// TestDetect_NonZeroExitIsNotHardFailure pins the contract: a non-zero exit
// from one method must NOT short-circuit; the detector moves to the next.
func TestDetect_NonZeroExitIsNotHardFailure(t *testing.T) {
	t.Parallel()
	r := &fakeRunner{
		responses: []fakeResponse{
			{matchPrefix: "cat /etc/frameworks/inventory.json", exitCode: 1},
			{matchPrefix: "docker ps -a", exitCode: 1},
			{matchPrefix: "systemctl show", exitCode: 0, stdout: "LoadState=loaded\nActiveState=active\nSubState=running"},
		},
	}
	d := newDetectorWithRunner(inventory.Host{ExternalIP: "1.2.3.4", User: "root"}, r)

	state, err := d.Detect(context.Background(), "foghorn")
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !state.Exists || state.Mode != "native" || !state.Running {
		t.Fatalf("got %+v, want native service detected as running", state)
	}
	// Chain must have progressed past inventory and docker to systemd.
	if len(r.calls) < 3 {
		t.Fatalf("expected the detector to try multiple methods, got %d calls", len(r.calls))
	}
}
