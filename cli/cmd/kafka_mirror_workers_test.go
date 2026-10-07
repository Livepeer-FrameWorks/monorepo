package cmd

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// The journal line and ss socket a staging follower (fw-stg-us-2, Kafka 4.2.0)
// printed while dedicated.mode.enable.internal.rest was off: one
// reconfiguration failure a minute and no REST listener.
const (
	stagingReconfigureFailureLine = "[2026-10-07 11:41:44,347] ERROR [Worker clientId=eu-west->us-east, groupId=eu-west-mm2] Failed to reconfigure connector's tasks (MirrorSourceConnector), retrying after backoff. (org.apache.kafka.connect.runtime.distributed.DistributedHerder:2167)\n" +
		"org.apache.kafka.connect.runtime.distributed.NotLeaderException: This worker is not able to communicate with the leader of the cluster, which is required for dynamically-reconfiguring connectors.\n"
	stagingRESTListener = "LISTEN 0      50     [::ffff:10.88.10.11]:8083             *:*    users:((\"java\",pid=404117,fd=120))\n"
)

// runMirrorMakerProbe runs the real probe script under sh with journalctl and
// ss replaced by stubs that print the given output.
func runMirrorMakerProbe(t *testing.T, journal, listeners string, journalExit int) (string, int) {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".out"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("journalctl", journal)
	write("ss", listeners)
	for name, exit := range map[string]int{"journalctl": journalExit, "ss": 0} {
		stub := "#!/bin/sh\ncat " + filepath.Join(dir, name+".out") + "\nexit " + strconv.Itoa(exit) + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(stub), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", mirrorMakerWorkerProbeScript(8083))
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	out, err := cmd.Output()
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(out), exitErr.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(out), 0
}

func TestMirrorMakerWorkerProbeCountsReconfigureFailures(t *testing.T) {
	out, code := runMirrorMakerProbe(t, strings.Repeat(stagingReconfigureFailureLine, 15), "", 0)
	if code != 0 {
		t.Fatalf("probe exit = %d, output %q", code, out)
	}
	probe, err := parseMirrorMakerWorkerProbe(out)
	if err != nil {
		t.Fatal(err)
	}
	if probe.ReconfigureFailures != 15 {
		t.Fatalf("ReconfigureFailures = %d, want 15 (output %q)", probe.ReconfigureFailures, out)
	}
	if probe.listensOn("10.88.10.11", 8083) {
		t.Fatalf("listensOn with no listener = true (%+v)", probe)
	}

	out, code = runMirrorMakerProbe(t, "[2026-10-07 12:00:00,000] INFO healthy\n", stagingRESTListener, 0)
	if code != 0 {
		t.Fatalf("probe exit = %d, output %q", code, out)
	}
	probe, err = parseMirrorMakerWorkerProbe(out)
	if err != nil {
		t.Fatal(err)
	}
	if probe.ReconfigureFailures != 0 || !probe.listensOn("10.88.10.11", 8083) {
		t.Fatalf("healthy worker probe = %+v (output %q)", probe, out)
	}
	if probe.listensOn("10.88.10.12", 8083) {
		t.Fatal("listener on another address counted as the advertised one")
	}
}

func TestMirrorMakerWorkerProbeFailsOnUnreadableJournal(t *testing.T) {
	out, code := runMirrorMakerProbe(t, "Failed to open journal\n", stagingRESTListener, 1)
	if code == 0 {
		t.Fatalf("probe with an unreadable journal exited 0: %q", out)
	}
}

// probeRunner answers the worker probe per host with output captured from the
// real probe script.
type probeRunner struct {
	host    string
	outputs map[string]string
}

func (r *probeRunner) Run(_ context.Context, command string) (*ssh.CommandResult, error) {
	if !strings.Contains(command, "frameworks-kafka-mirrormaker.service") {
		return &ssh.CommandResult{ExitCode: 1, Stderr: "unexpected command"}, nil
	}
	return &ssh.CommandResult{Stdout: r.outputs[r.host]}, nil
}
func (r *probeRunner) RunScript(context.Context, string) (*ssh.CommandResult, error) {
	return &ssh.CommandResult{ExitCode: 1}, nil
}
func (r *probeRunner) Upload(context.Context, ssh.UploadOptions) error { return nil }
func (r *probeRunner) Close() error                                    { return nil }

func TestCheckMirrorMakerWorkersFlagsFollowerThatCannotReconfigure(t *testing.T) {
	manifest := multiRegionReleaseManifest()
	healthy, _ := runMirrorMakerProbe(t, "", strings.ReplaceAll(stagingRESTListener, "10.88.10.11", "10.88.1.11"), 0)
	broken, _ := runMirrorMakerProbe(t, strings.Repeat(stagingReconfigureFailureLine, 15), "", 0)
	outputs := map[string]string{"regional-eu-1": healthy, "regional-us-1": broken}
	result := checkMirrorMakerWorkers(context.Background(), manifest, func(host inventory.Host) (ssh.Runner, error) {
		return &probeRunner{host: host.Name, outputs: outputs}, nil
	})
	if result == nil || result.OK {
		t.Fatalf("result = %+v, want unhealthy", result)
	}
	for _, want := range []string{
		"regional-us-1: internal REST server is not listening on 10.88.10.11:8083",
		"regional-us-1: 15 \"Failed to reconfigure connector's tasks\"",
	} {
		if !strings.Contains(result.Error, want) {
			t.Errorf("error missing %q:\n%s", want, result.Error)
		}
	}
	if strings.Contains(result.Error, "regional-eu-1") {
		t.Errorf("healthy worker reported:\n%s", result.Error)
	}

	outputs["regional-us-1"], _ = runMirrorMakerProbe(t, "", stagingRESTListener, 0)
	result = checkMirrorMakerWorkers(context.Background(), manifest, func(host inventory.Host) (ssh.Runner, error) {
		return &probeRunner{host: host.Name, outputs: outputs}, nil
	})
	if result == nil || !result.OK {
		t.Fatalf("result = %+v, want healthy", result)
	}
}

func TestCheckMirrorMakerWorkersSkipsClusterWithoutMirrorMaker(t *testing.T) {
	manifest := multiRegionReleaseManifest()
	manifest.Infrastructure.Kafka.MirrorMaker = nil
	if result := checkMirrorMakerWorkers(context.Background(), manifest, nil); result != nil {
		t.Fatalf("result = %+v, want nil without MirrorMaker2", result)
	}
}
