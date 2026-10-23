package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/inventory"
	fwssh "frameworks/cli/pkg/ssh"
)

// `sudo ss -Hltunp` on staging's fw-stg-edge-eu before Mist's HTTP connector
// moved to loopback: MistOutHTTP on *:8080 answered plain HTTP past Caddy.
// MistOutWebRTC holds its configured port 18203 and, on ephemeral port 64142,
// the listener's STUN relay socket, left unbound until its first send put it
// on 0.0.0.0 (MistServer before a16851c86).
const stagingEdgeListeners = `udp UNCONN 0      0                                0.0.0.0:8889  0.0.0.0:* users:(("MistOutTSSRT",pid=250550,fd=0))
udp UNCONN 0      0                                0.0.0.0:18203 0.0.0.0:* users:(("MistOutWebRTC",pid=250549,fd=0))
udp UNCONN 0      0                                0.0.0.0:64142 0.0.0.0:* users:(("MistOutWebRTC",pid=250549,fd=4))
udp UNCONN 0      0                             127.0.0.54:53    0.0.0.0:* users:(("systemd-resolve",pid=680,fd=16))
udp UNCONN 0      0                          127.0.0.53%lo:53    0.0.0.0:* users:(("systemd-resolve",pid=680,fd=14))
udp UNCONN 0      0                    192.168.10.23%ens18:68    0.0.0.0:* users:(("systemd-network",pid=633,fd=24))
udp UNCONN 0      0                                      *:443         *:* users:(("caddy",pid=769,fd=7))
udp UNCONN 0      0      [fe80::be24:11ff:fe4e:8f5e]%ens18:546      [::]:* users:(("systemd-network",pid=633,fd=18))
udp UNCONN 0      0                     [::ffff:127.0.0.1]:4242        *:* users:(("MistOutTSSRT",pid=250550,fd=32),("MistOutWebRTC",pid=250549,fd=32),("MistController",pid=250483,fd=32))
tcp LISTEN 0      16384                         127.0.0.54:53    0.0.0.0:* users:(("systemd-resolve",pid=680,fd=17))
tcp LISTEN 0      16384                          127.0.0.1:2019  0.0.0.0:* users:(("caddy",pid=769,fd=9))
tcp LISTEN 0      16384                          127.0.0.1:18007 0.0.0.0:* users:(("helmsman",pid=250466,fd=9))
tcp LISTEN 0      16384                          127.0.0.1:18017 0.0.0.0:* users:(("helmsman",pid=250466,fd=11))
tcp LISTEN 0      16384                            0.0.0.0:22    0.0.0.0:* users:(("sshd",pid=914,fd=3),("systemd",pid=1,fd=107))
tcp LISTEN 0      16384                          127.0.0.1:8429  0.0.0.0:* users:(("vmagent",pid=19861,fd=8))
tcp LISTEN 0      500                            127.0.0.1:4242  0.0.0.0:* users:(("MistController",pid=250483,fd=0))
tcp LISTEN 0      500                                    *:8080        *:* users:(("MistOutHTTP",pid=60988,fd=0))
tcp LISTEN 0      16384                                  *:443         *:* users:(("caddy",pid=769,fd=6))
tcp LISTEN 0      16384                                  *:80          *:* users:(("caddy",pid=769,fd=10))
tcp LISTEN 0      500                                    *:4200        *:* users:(("MistOutDTSC",pid=60987,fd=0))
tcp LISTEN 0      16384                               [::]:22       [::]:* users:(("sshd",pid=914,fd=4),("systemd",pid=1,fd=108))
tcp LISTEN 0      500                                    *:5554        *:* users:(("MistOutRTSP",pid=60990,fd=0))
tcp LISTEN 0      500                                    *:1935        *:* users:(("MistOutRTMP",pid=60989,fd=0))`

// The same edge's ephemeral port range, no WHIP push out running, and the
// Mist protocol ports its /etc/mistserver.conf sets (TSSRT and WebRTC pinned,
// the rest default).
const stagingEdgeProbeTail = `
@@ip_local_port_range 10000	65535
@@whip_push_pids
@@mistserver_config
{"config":{"protocols":[{"connector":"DTSC"},{"connector":"HTTP"},{"connector":"RTMP"},{"connector":"RTSP"},{"connector":"TSSRT","port":8889},{"connector":"WebRTC","port":18203}]}}`

func edgeExposed(t *testing.T, probe string) []string {
	t.Helper()
	listeners, policy, err := parseEdgeListenerProbe(probe)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range parseWildcardListenersAllowing(listeners, policy.allows) {
		out = append(out, l.Proto+":"+strconv.Itoa(l.Port))
	}
	slices.Sort(out)
	return out
}

func TestEdgeListenerExposureFlagsStagingMistHTTPAndSTUNRelay(t *testing.T) {
	got := edgeExposed(t, stagingEdgeListeners+stagingEdgeProbeTail)
	if !slices.Equal(got, []string{"tcp:8080", "udp:64142"}) {
		t.Fatalf("flagged %v, want tcp:8080 (MistOutHTTP on every interface) and udp:64142 (the WebRTC listener's wildcard STUN relay)", got)
	}

	// Mist HTTP on loopback, and the relay connected to 224.0.0.224 (ss lists
	// a connected UDP socket as ESTAB, which `ss -l` leaves out).
	fixed := strings.Replace(stagingEdgeListeners, "*:8080        *:*", "127.0.0.1:8080  0.0.0.0:*", 1)
	fixed = strings.Replace(fixed, "udp UNCONN 0      0                                0.0.0.0:64142 0.0.0.0:* users:((\"MistOutWebRTC\",pid=250549,fd=4))\n", "", 1)
	if got := edgeExposed(t, fixed+stagingEdgeProbeTail); len(got) != 0 {
		t.Fatalf("flagged %v on an edge with Mist HTTP on loopback and a connected STUN relay", got)
	}
}

// Staging doctor on edge-eu, 2026-10-22: "udp 0.0.0.0:18463 (MistOutWebRTC)",
// the WebRTC listener's STUN relay on an ephemeral port. Only a WHIP push
// out's process may hold a wildcard socket in the ephemeral range.
func TestEdgeListenerExposureAllowsEphemeralWebRTCOnlyForWHIPPushOut(t *testing.T) {
	extra := "\nudp UNCONN 0 0 0.0.0.0:18463 0.0.0.0:* users:((\"MistOutWebRTC\",pid=250549,fd=5))" + // the listener's relay
		"\nudp UNCONN 0 0 0.0.0.0:41000 0.0.0.0:* users:((\"MistOutWebRTC\",pid=300,fd=7))" + // a WHEP session
		"\nudp UNCONN 0 0 0.0.0.0:42000 0.0.0.0:* users:((\"MistOutWebRTC\",pid=400,fd=7))" + // a WHIP push out
		"\nudp UNCONN 0 0 0.0.0.0:9000 0.0.0.0:* users:((\"MistOutWebRTC\",pid=401,fd=7))" + // a WHIP push out below the range
		"\nudp UNCONN 0 0 0.0.0.0:40000 0.0.0.0:* users:((\"python3\",pid=400,fd=3))" + // in range, not WebRTC
		"\ntcp LISTEN 0 500 *:18203 *:* users:((\"MistOutWebRTC\",pid=250549,fd=6))" // WebRTC port is UDP only
	fixed := strings.Replace(stagingEdgeListeners, "*:8080        *:*", "127.0.0.1:8080  0.0.0.0:*", 1)
	tail := strings.Replace(stagingEdgeProbeTail, "@@whip_push_pids", "@@whip_push_pids 400 401", 1)
	got := edgeExposed(t, fixed+extra+tail)
	want := []string{"tcp:18203", "udp:18463", "udp:40000", "udp:41000", "udp:64142", "udp:9000"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("flagged %v, want %v", got, want)
	}

	// A WebRTC port moved in the Mist config moves the allowance with it.
	moved := strings.Replace(tail, `"port":18203`, `"port":19000`, 1)
	if got := edgeExposed(t, fixed+moved); !slices.Contains(got, "udp:18203") {
		t.Fatalf("flagged %v, want udp:18203 once the config moves WebRTC to 19000", got)
	}
	// Without a readable port range no ephemeral WebRTC socket is allowed.
	noRange := strings.Replace(tail, "10000\t65535", "", 1)
	if got := edgeExposed(t, fixed+extra+noRange); !slices.Contains(got, "udp:42000") {
		t.Fatalf("flagged %v, want the WHIP push out's udp:42000 without a port range", got)
	}
}

// TestWHIPPushPIDsShellNamesOnlyPushOuts runs the probe's PID listing on a
// Linux host against two processes named MistOutWebRTC: one with a whip://
// argument, as Mist starts a push out, and one without, as a session runs.
func TestWHIPPushPIDsShellNamesOnlyPushOuts(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	shell, err := filepath.EvalSymlinks("/bin/sh")
	if err != nil {
		t.Skip("no /bin/sh")
	}
	body, err := os.ReadFile(shell)
	if err != nil {
		t.Fatal(err)
	}
	mist := filepath.Join(t.TempDir(), mistWebRTCProcess)
	if err = os.WriteFile(mist, body, 0o755); err != nil {
		t.Fatal(err)
	}
	start := func(arg string) *exec.Cmd {
		// The trailing ":" keeps the shell from exec'ing sleep in its place.
		cmd := exec.CommandContext(t.Context(), mist, "-c", "sleep 30; :", arg)
		if startErr := cmd.Start(); startErr != nil {
			t.Fatal(startErr)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		return cmd
	}
	push := start("whip://push.example/live")
	session := start("--stream")

	var out []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		out, err = exec.CommandContext(t.Context(), "/bin/sh", "-c", whipPushPIDsShell).Output()
		if err != nil {
			t.Fatalf("whip push PID listing: %v", err)
		}
		if slices.Contains(strings.Fields(string(out)), strconv.Itoa(push.Process.Pid)) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	pids := strings.Fields(string(out))
	if !slices.Contains(pids, strconv.Itoa(push.Process.Pid)) || slices.Contains(pids, strconv.Itoa(session.Process.Pid)) {
		t.Fatalf("whip push PIDs = %v, want %d (push out) and not %d (session)", pids, push.Process.Pid, session.Process.Pid)
	}
}

func TestParseWildcardListenersReadsTheOwningPID(t *testing.T) {
	got := parseWildcardListenersAllowing(stagingEdgeListeners, func(exposedListener) bool { return false })
	for _, l := range got {
		if l.Port == 64142 && (l.Process != "MistOutWebRTC" || l.PID != 250549) {
			t.Fatalf("udp 64142 owner = %q pid %d, want MistOutWebRTC pid 250549", l.Process, l.PID)
		}
	}
}

func TestEdgeListenerExposureNeedsTheMistConfig(t *testing.T) {
	if _, _, err := parseEdgeListenerProbe(stagingEdgeListeners + "\n@@ip_local_port_range 10000 65535\n@@whip_push_pids \n@@mistserver_config\ncat: /etc/mistserver.conf: Permission denied"); err == nil {
		t.Fatal("an unreadable Mist config must fail the check rather than guess the public ports")
	}
}

type fakeListenerRunner struct {
	fwssh.Runner
	out string
}

func (f fakeListenerRunner) Run(context.Context, string) (*fwssh.CommandResult, error) {
	return &fwssh.CommandResult{Stdout: f.out}, nil
}

// cluster doctor covers the edge manifest's nodes: an edge target gets the
// edge policy, not the platform host's (which would flag RTMP, DTSC and the
// rest of the public media ports).
func TestDoctorListenerChecksCoverEdges(t *testing.T) {
	manifest := &inventory.Manifest{Hosts: map[string]inventory.Host{"core-1": {}}}
	checks := doctorListenerChecks(nil, manifest, []diagnoseHost{{Name: "core-1"}, {Name: "fw-stg-edge-eu", Edge: true}})
	if len(checks) != 2 || checks[1].name != "Listeners on fw-stg-edge-eu (edge)" {
		t.Fatalf("checks = %+v, want the platform host and the edge", checks)
	}
	result := checkEdgeListenerExposure(context.Background(), func() (fwssh.Runner, error) {
		return fakeListenerRunner{out: stagingEdgeListeners + stagingEdgeProbeTail}, nil
	})
	if result.OK || !strings.Contains(result.Error, "tcp *:8080 (MistOutHTTP)") || strings.Contains(result.Error, "1935") {
		t.Fatalf("edge result = %+v, want only Mist HTTP flagged", result)
	}
}
