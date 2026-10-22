package cmd

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"frameworks/cli/pkg/inventory"
	fwssh "frameworks/cli/pkg/ssh"
)

// `sudo ss -Hltunp` on staging's fw-stg-edge-eu before Mist's HTTP connector
// moved to loopback: MistOutHTTP on *:8080 answered plain HTTP past Caddy.
// MistOutWebRTC holds its configured port 18203 and, on ephemeral port 64142,
// the listener's STUN forwarder socket.
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

// The same edge's ephemeral port range and the Mist protocol ports its
// /etc/mistserver.conf sets (TSSRT and WebRTC pinned, the rest default).
const stagingEdgeProbeTail = `
@@ip_local_port_range 10000	65535
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

func TestEdgeListenerExposureFlagsStagingMistHTTP(t *testing.T) {
	got := edgeExposed(t, stagingEdgeListeners+stagingEdgeProbeTail)
	if !slices.Equal(got, []string{"tcp:8080"}) {
		t.Fatalf("flagged %v, want only tcp:8080 (MistOutHTTP on every interface)", got)
	}

	fixed := strings.Replace(stagingEdgeListeners, "*:8080        *:*", "127.0.0.1:8080  0.0.0.0:*", 1)
	if got := edgeExposed(t, fixed+stagingEdgeProbeTail); len(got) != 0 {
		t.Fatalf("flagged %v on an edge with Mist HTTP on loopback", got)
	}
}

func TestEdgeListenerExposureAllowsOnlyConfiguredWebRTCRange(t *testing.T) {
	extra := "\nudp UNCONN 0 0 0.0.0.0:9000 0.0.0.0:* users:((\"MistOutWebRTC\",pid=1,fd=5))" + // below the ephemeral range
		"\nudp UNCONN 0 0 0.0.0.0:40000 0.0.0.0:* users:((\"python3\",pid=2,fd=3))" + // in range, not WebRTC
		"\ntcp LISTEN 0 500 *:18203 *:* users:((\"MistOutWebRTC\",pid=1,fd=6))" // WebRTC port is UDP only
	got := edgeExposed(t, stagingEdgeListeners+extra+stagingEdgeProbeTail)
	want := []string{"tcp:18203", "tcp:8080", "udp:40000", "udp:9000"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("flagged %v, want %v", got, want)
	}

	// A WebRTC port moved in the Mist config moves the allowance with it
	// (on a host whose ephemeral range does not cover the old port).
	moved := strings.Replace(strings.Replace(stagingEdgeProbeTail, `"port":18203`, `"port":19000`, 1), "10000\t65535", "32768 60999", 1)
	if got := edgeExposed(t, stagingEdgeListeners+moved); !slices.Contains(got, "udp:18203") || !slices.Contains(got, "udp:64142") {
		t.Fatalf("flagged %v, want udp:18203 once the config moves WebRTC to 19000 and udp:64142 outside 32768-60999", got)
	}
	// Without a readable port range no ephemeral WebRTC socket is allowed.
	noRange := strings.Replace(stagingEdgeProbeTail, "10000\t65535", "", 1)
	if got := edgeExposed(t, stagingEdgeListeners+noRange); !slices.Contains(got, "udp:64142") {
		t.Fatalf("flagged %v, want udp:64142 without a port range", got)
	}
}

func TestEdgeListenerExposureNeedsTheMistConfig(t *testing.T) {
	if _, _, err := parseEdgeListenerProbe(stagingEdgeListeners + "\n@@ip_local_port_range 10000 65535\n@@mistserver_config\ncat: /etc/mistserver.conf: Permission denied"); err == nil {
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
