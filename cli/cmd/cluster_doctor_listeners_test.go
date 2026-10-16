package cmd

import (
	"slices"
	"strconv"
	"testing"

	"frameworks/cli/pkg/inventory"
)

// Listeners `ss -Hltunp` reported on staging's core host before the bind
// fix: Go services, PostgreSQL, the metrics stack, Keeper's Raft port, Kafka's
// JMX RMI ports and a stock MistServer on every interface, beside the
// listeners that may be public and the ones already on loopback or the mesh.
const stagingCoreListeners = `tcp   LISTEN 0      4096       127.0.0.1:46205      0.0.0.0:*    users:(("containerd",pid=855,fd=15))
tcp   LISTEN 0      4096   10.89.236.242:9000       0.0.0.0:*    users:(("clickhouse-serv",pid=2678869,fd=57))
tcp   LISTEN 0      400          0.0.0.0:5432       0.0.0.0:*    users:(("postgres",pid=982936,fd=6))
tcp   LISTEN 0      4096         0.0.0.0:22         0.0.0.0:*    users:(("sshd",pid=992701,fd=3))
tcp   LISTEN 0      511          0.0.0.0:443        0.0.0.0:*    users:(("nginx",pid=982880,fd=7))
tcp   LISTEN 0      511          0.0.0.0:80         0.0.0.0:*    users:(("nginx",pid=982880,fd=6))
tcp   LISTEN 0      511          0.0.0.0:18090      0.0.0.0:*    users:(("nginx",pid=982880,fd=5))
tcp   LISTEN 0      4096         0.0.0.0:8428       0.0.0.0:*    users:(("victoria-metric",pid=1100,fd=100))
tcp   LISTEN 0      4096         0.0.0.0:3000       0.0.0.0:*    users:(("docker-proxy",pid=352719,fd=7))
tcp   LISTEN 0      500                *:4242             *:*    users:(("MistController",pid=834,fd=0))
tcp   LISTEN 0      4096               *:19001            *:*    users:(("commodore",pid=1059645,fd=10))
tcp   LISTEN 0      128                *:9234             *:*    users:(("clickhouse-keep",pid=1080,fd=45))
tcp   LISTEN 0      4096               *:35065            *:*    users:(("java",pid=2001,fd=40))
tcp   LISTEN 0      400             [::]:5432          [::]:*    users:(("postgres",pid=982936,fd=7))
tcp   LISTEN 0      4096               *:18029            *:*    users:(("foghorn",pid=3001,fd=9))
tcp   LISTEN 0      4096       127.0.0.1:18024      0.0.0.0:*    users:(("network-observe",pid=1101267,fd=14))
udp   UNCONN 0      0            0.0.0.0:51820      0.0.0.0:*
udp   UNCONN 0      0                  *:18203            *:*    users:(("MistOutWebRTC",pid=1067,fd=0))
udp   UNCONN 0      0    192.168.10.16%ens18:68         0.0.0.0:*    users:(("systemd-network",pid=977865,fd=24))
udp   UNCONN 0      0         127.0.0.53%lo:53         0.0.0.0:*    users:(("systemd-resolve",pid=978167,fd=14))`

func TestParseWildcardListenersFlagsEveryExposedListener(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts:      map[string]inventory.Host{"core-1": {WireguardPort: 51820}},
		Interfaces: map[string]inventory.ServiceConfig{"nginx": {Enabled: true, Host: "core-1"}},
	}
	flagged := func() map[string]bool {
		tcp, udp := publicListenerPorts(manifest, "core-1")
		out := map[string]bool{}
		for _, l := range parseWildcardListeners(stagingCoreListeners, tcp, udp) {
			out[l.Proto+":"+strconv.Itoa(l.Port)] = true
		}
		return out
	}
	want := []string{"tcp:3000", "tcp:4242", "tcp:5432", "tcp:8428", "tcp:9234", "tcp:18029", "tcp:18090", "tcp:19001", "tcp:35065", "udp:18203"}
	got := flagged()
	var gotKeys []string
	for key := range got {
		gotKeys = append(gotKeys, key)
	}
	slices.Sort(gotKeys)
	slices.Sort(want)
	// SSH, the proxy's 80/443 and WireGuard are public; loopback, mesh and
	// interface-bound sockets are not on every interface.
	if !slices.Equal(gotKeys, want) {
		t.Fatalf("flagged %v, want %v", gotKeys, want)
	}
	// Foghorn's external gRPC port is public on a Foghorn host.
	manifest.Services = map[string]inventory.ServiceConfig{"foghorn-eu": {Enabled: true, Deploy: "foghorn", Host: "core-1"}}
	if flagged()["tcp:18029"] {
		t.Fatal("Foghorn's external gRPC port flagged on a Foghorn host")
	}
}
