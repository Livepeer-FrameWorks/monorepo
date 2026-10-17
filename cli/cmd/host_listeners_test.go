package cmd

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/health"
)

// /proc/net tables from a stack host container, which ships no ss: sshd on
// 0.0.0.0:22, a service on 127.0.0.1:18008, an established connection, a
// wildcard tcp6 listener on 9100, and the WireGuard socket on udp 51820.
const procNetSample = procNetMarker + ` tcp
  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1001 1 0000000000000000 100 0 0 10 0
   1: 0100007F:4658 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1002 1 0000000000000000 100 0 0 10 0
   2: 0100007F:4658 0100007F:D2F0 01 00000000:00000000 00:00000000 00000000     0        0 1003 1 0000000000000000 20 4 30 10 -1
` + procNetMarker + ` tcp6
  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:238C 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1004 1 0000000000000000 100 0 0 10 0
` + procNetMarker + ` udp
  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
  10: 00000000:CA6C 00000000:0000 07 00000000:00000000 00:00000000 00000000     0        0 1005 2 0000000000000000 0
` + procNetMarker + ` udp6
  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode ref pointer drops
`

func TestNormalizeHostListenersDecodesProcNetTables(t *testing.T) {
	got := normalizeHostListeners(procNetSample)
	want := "tcp LISTEN 0 0 0.0.0.0:22 *:*\n" +
		"tcp LISTEN 0 0 127.0.0.1:18008 *:*\n" +
		"tcp LISTEN 0 0 [::]:9100 *:*\n" +
		"udp UNCONN 0 0 0.0.0.0:51820 *:*\n"
	if got != want {
		t.Fatalf("normalized /proc/net tables:\n%s\nwant:\n%s", got, want)
	}

	listening := listeningPorts(got)
	for _, port := range []int{22, 18008, 9100} {
		if !listening.has("tcp", port) {
			t.Errorf("diagnose ports does not see tcp listener %d from /proc/net", port)
		}
	}
	if !listening.has("udp", 51820) {
		t.Error("diagnose ports does not see the udp 51820 socket from /proc/net")
	}

	exposed := parseWildcardListeners(got, map[int]bool{22: true}, map[int]bool{51820: true})
	if len(exposed) != 1 || exposed[0].Port != 9100 || exposed[0].Proto != "tcp" {
		t.Fatalf("doctor exposure from /proc/net = %v, want only tcp [::]:9100", exposed)
	}

	edge := edgeListenerExposureResult(&health.CheckResult{}, procNetSample+
		edgePortRangeMarker+" 32768 60999\n"+edgeMistConfigMarker+"\n"+`{"config":{"protocols":[]}}`)
	if edge.OK || !strings.Contains(edge.Error, "9100") {
		t.Fatalf("edge doctor from /proc/net: ok=%v err=%q, want the wildcard 9100 listener reported", edge.OK, edge.Error)
	}
}

func TestNormalizeHostListenersKeepsSSOutput(t *testing.T) {
	ss := "tcp   LISTEN 0      4096   0.0.0.0:22   0.0.0.0:*  users:((\"sshd\",pid=1,fd=3))\n"
	if got := normalizeHostListeners(ss); got != ss {
		t.Fatalf("ss output changed: %q", got)
	}
}

// TestHostListenerShellWorksWithoutSS runs the listener command the doctor and
// `cluster diagnose ports` send over SSH on a Linux host whose PATH has no ss:
// it must still report a wildcard and a loopback listener this test opens.
func TestHostListenerShellWorksWithoutSS(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc/net")
	}
	cat, err := exec.LookPath("cat")
	if err != nil {
		t.Skip("no cat")
	}
	bin := t.TempDir()
	if err = os.Symlink(cat, filepath.Join(bin, "cat")); err != nil {
		t.Fatal(err)
	}
	wildcard, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer wildcard.Close()
	loopback, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer loopback.Close()

	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", hostListenerShell)
	cmd.Env = []string{"PATH=" + bin}
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("listener command without ss: %v\n%s", err, out)
	}
	listening := listeningPorts(normalizeHostListeners(string(out)))
	for _, l := range []net.Listener{wildcard, loopback} {
		port := l.Addr().(*net.TCPAddr).Port
		if !listening.has("tcp", port) {
			t.Errorf("listener %s missing from the output of a host without ss:\n%s", l.Addr(), out)
		}
	}
	wildcardPort := wildcard.Addr().(*net.TCPAddr).Port
	found := false
	for _, l := range parseWildcardListeners(normalizeHostListeners(string(out)), nil, nil) {
		if l.Port == wildcardPort {
			found = true
		}
		if l.Port == loopback.Addr().(*net.TCPAddr).Port {
			t.Errorf("loopback listener %s reported as wildcard", strconv.Itoa(l.Port))
		}
	}
	if !found {
		t.Errorf("wildcard listener %d not reported as exposed", wildcardPort)
	}
}
