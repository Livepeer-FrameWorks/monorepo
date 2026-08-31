//go:build schema_verify

package redis

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockervalkey"
)

func TestSentinelFailover_RealValkey(t *testing.T) {
	image, err := dockervalkey.Image()
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("fw-valkey-ha-%d", time.Now().UnixNano())
	networkName := prefix + "-net"
	dataNames := []string{prefix + "-master", prefix + "-replica-1", prefix + "-replica-2"}
	sentinelNames := []string{prefix + "-sentinel-1", prefix + "-sentinel-2", prefix + "-sentinel-3"}
	if out, err := dockerpg.CLI("network", "create", networkName); err != nil {
		t.Fatalf("create network: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		for _, name := range append(dataNames, sentinelNames...) {
			_, _ = dockerpg.CLI("rm", "-fv", name)
		}
		_, _ = dockerpg.CLI("network", "rm", networkName)
	})

	startData := func(name string, replica bool) {
		args := []string{"run", "-d", "--name", name, "--network", networkName, "-P", image,
			"valkey-server", "--appendonly", "yes"}
		if replica {
			args = append(args, "--replicaof", dataNames[0], "6379")
		}
		if out, runErr := dockerpg.Run(args...); runErr != nil {
			t.Fatalf("start %s: %v\n%s", name, runErr, out)
		}
	}
	startData(dataNames[0], false)
	startData(dataNames[1], true)
	startData(dataNames[2], true)

	// Sentinel rewrites its configuration during failover. Create it inside a writable
	// container tmpfs so the runtime UID owns the file on both Docker Desktop and Linux CI.
	for index, name := range sentinelNames {
		config := fmt.Sprintf("port 26379\ndir /sentinel\nsentinel resolve-hostnames yes\nsentinel monitor frameworks-master %s 6379 2\nsentinel down-after-milliseconds frameworks-master 1000\nsentinel failover-timeout frameworks-master 8000\nsentinel parallel-syncs frameworks-master 1\n", dataNames[0])
		configWriter := `printf '%s' "$1" > /sentinel/sentinel.conf && exec valkey-server /sentinel/sentinel.conf --sentinel`
		if out, runErr := dockerpg.Run("run", "-d", "--name", name, "--network", networkName, "--expose", "26379", "-P",
			"--tmpfs", "/sentinel:rw,mode=1777", image, "sh", "-c", configWriter, fmt.Sprintf("sentinel-%d", index), config); runErr != nil {
			t.Fatalf("start %s: %v\n%s", name, runErr, out)
		}
	}

	waitDockerCommand(t, 45*time.Second, func() bool {
		out, commandErr := dockerpg.CLI("exec", dataNames[0], "valkey-cli", "info", "replication")
		if commandErr != nil || !strings.Contains(out, "connected_slaves:2") {
			return false
		}
		for _, replica := range dataNames[1:] {
			role, roleErr := dockerpg.CLI("exec", replica, "valkey-cli", "--raw", "role")
			if roleErr != nil || !strings.Contains(role, "slave\n") || !strings.Contains(role, "connected") {
				return false
			}
		}
		return true
	}, "two connected replicas")

	diagnostics := func() string {
		return sentinelDiagnostics(dataNames, sentinelNames, "frameworks-master")
	}
	waitDockerCommandWithDiagnostics(t, 45*time.Second, func() bool {
		for _, sentinel := range sentinelNames {
			quorum, quorumErr := dockerpg.CLI("exec", sentinel, "valkey-cli", "-p", "26379", "--raw", "sentinel", "ckquorum", "frameworks-master")
			if quorumErr != nil || !strings.HasPrefix(strings.TrimSpace(quorum), "OK") {
				return false
			}
			master, masterErr := sentinelMasterState(sentinel, "frameworks-master")
			if masterErr != nil || master["num-slaves"] != "2" || master["num-other-sentinels"] != "2" || strings.Contains(master["flags"], "down") {
				return false
			}
		}
		return true
	}, "all Sentinels to establish quorum and discover two replicas", diagnostics)

	addressMap := make(map[string]string)
	for _, name := range dataNames {
		internal := containerAddress(t, name, "6379")
		port, portErr := dockerpg.DiscoverPublishedHostPort(name, "6379/tcp")
		if portErr != nil {
			t.Fatal(portErr)
		}
		addressMap[internal] = "127.0.0.1:" + port
	}
	var sentinelAddrs []string
	for _, name := range sentinelNames {
		port, portErr := dockerpg.DiscoverPublishedHostPort(name, "26379/tcp")
		if portErr != nil {
			t.Fatal(portErr)
		}
		sentinelAddrs = append(sentinelAddrs, "127.0.0.1:"+port)
	}
	dialer := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if mapped := addressMap[addr]; mapped != "" {
			addr = mapped
		}
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, addr)
	}
	client, err := newUniversalClient(context.Background(), Config{
		Mode: ModeSentinel, Addrs: sentinelAddrs, MasterName: "frameworks-master",
		DialTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second,
	}, dialer)
	if err != nil {
		t.Fatalf("connect through Sentinel: %v", err)
	}
	defer func() { _ = client.Close() }()
	ctx := context.Background()
	if err = client.Set(ctx, "ha-contract", "before-failover", 0).Err(); err != nil {
		t.Fatalf("write before failover: %v", err)
	}
	var acknowledged int
	var waitErr error
	waitDockerCommandWithDiagnostics(t, 45*time.Second, func() bool {
		acknowledged, waitErr = client.Do(ctx, "WAIT", 2, 5000).Int()
		return waitErr == nil && acknowledged == 2
	}, "both replicas to acknowledge the pre-failover write", func() string {
		return fmt.Sprintf("last WAIT result: acknowledged=%d err=%v\n%s", acknowledged, waitErr, diagnostics())
	})
	oldMaster := containerAddress(t, dataNames[0], "6379")
	if out, stopErr := dockerpg.CLI("stop", "-t", "1", dataNames[0]); stopErr != nil {
		t.Fatalf("stop primary: %v\n%s", stopErr, out)
	}
	var promotedMaster string
	waitDockerCommandWithDiagnostics(t, 45*time.Second, func() bool {
		promotedMaster = ""
		for _, sentinel := range sentinelNames {
			current, currentErr := sentinelMasterAddress(sentinel, "frameworks-master")
			if currentErr != nil || current == "" || current == oldMaster {
				return false
			}
			if promotedMaster != "" && current != promotedMaster {
				return false
			}
			promotedMaster = current
		}
		return true
	}, "all Sentinels to agree on a promoted primary", diagnostics)
	var lastSetErr error
	waitDockerCommandWithDiagnostics(t, 30*time.Second, func() bool {
		lastSetErr = client.Set(ctx, "ha-contract", "after-failover", 0).Err()
		return lastSetErr == nil
	}, "Sentinel client to write through promoted primary", func() string {
		return fmt.Sprintf("promoted master=%s; last client SET error=%v\n%s", promotedMaster, lastSetErr, diagnostics())
	})
	if value, getErr := client.Get(ctx, "ha-contract").Result(); getErr != nil || value != "after-failover" {
		t.Fatalf("read after failover: value=%q err=%v", value, getErr)
	}
	if out, startErr := dockerpg.CLI("start", dataNames[0]); startErr != nil {
		t.Fatalf("restart former primary: %v\n%s", startErr, out)
	}
	waitDockerCommand(t, 45*time.Second, func() bool {
		out, commandErr := dockerpg.CLI("exec", dataNames[0], "valkey-cli", "role")
		return commandErr == nil && strings.HasPrefix(strings.TrimSpace(out), "slave")
	}, "former primary to rejoin as replica")
}

func TestChangelogReplayAndGap_RealValkey(t *testing.T) {
	engine := dockervalkey.Start(t)
	ctx := context.Background()
	type event struct {
		Value string `json:"value"`
	}
	log := NewChangelog[event](engine.Client, "{changelog-contract}:events", 100)
	tail, err := log.Tail(ctx)
	if err != nil || tail != "0-0" {
		t.Fatalf("empty tail=%q err=%v", tail, err)
	}
	first, err := log.Append(ctx, event{Value: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := log.Append(ctx, event{Value: "second"})
	if err != nil || CompareStreamIDs(second, first) <= 0 {
		t.Fatalf("stream IDs not monotonic: first=%q second=%q err=%v", first, second, err)
	}
	readCtx, cancel := context.WithCancel(ctx)
	seen := make(chan string, 1)
	go func() {
		_ = log.Read(readCtx, first, func(_ string, item event) {
			seen <- item.Value
			cancel()
		})
	}()
	select {
	case value := <-seen:
		if value != "second" {
			t.Fatalf("replayed value=%q, want second", value)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for changelog replay")
	}
	if err = engine.Client.XTrimMaxLen(ctx, log.Key(), 1).Err(); err != nil {
		t.Fatal(err)
	}
	if gap, gapErr := log.gapBehind(ctx, first); gapErr != nil || !gap {
		t.Fatalf("trimmed cursor gap=%v err=%v, want gap", gap, gapErr)
	}
	engine.Restart(t)
	log = NewChangelog[event](engine.Client, "{changelog-contract}:events", 100)
	if got, tailErr := log.Tail(ctx); tailErr != nil || got != second {
		t.Fatalf("container replacement lost stream: got=%q want=%q err=%v", got, second, tailErr)
	}
}

func TestClusterRouting_RealValkey(t *testing.T) {
	image, err := dockervalkey.Image()
	if err != nil {
		t.Fatal(err)
	}
	prefix := fmt.Sprintf("fw-valkey-cluster-%d", time.Now().UnixNano())
	networkName := prefix + "-net"
	names := []string{prefix + "-1", prefix + "-2", prefix + "-3"}
	if out, createErr := dockerpg.CLI("network", "create", networkName); createErr != nil {
		t.Fatalf("create cluster network: %v\n%s", createErr, out)
	}
	t.Cleanup(func() {
		for _, name := range names {
			_, _ = dockerpg.CLI("rm", "-fv", name)
		}
		_, _ = dockerpg.CLI("network", "rm", networkName)
	})
	for _, name := range names {
		if out, runErr := dockerpg.Run("run", "-d", "--name", name, "--network", networkName, "-P", image,
			"valkey-server", "--cluster-enabled", "yes", "--cluster-config-file", "nodes.conf", "--cluster-node-timeout", "5000"); runErr != nil {
			t.Fatalf("start cluster node %s: %v\n%s", name, runErr, out)
		}
		waitDockerCommand(t, 30*time.Second, func() bool {
			_, pingErr := dockerpg.CLI("exec", name, "valkey-cli", "ping")
			return pingErr == nil
		}, "cluster node "+name)
	}

	addressMap := make(map[string]string)
	internalAddrs := make([]string, 0, len(names))
	hostAddrs := make([]string, 0, len(names))
	for _, name := range names {
		internal := containerAddress(t, name, "6379")
		port, portErr := dockerpg.DiscoverPublishedHostPort(name, "6379/tcp")
		if portErr != nil {
			t.Fatal(portErr)
		}
		host := "127.0.0.1:" + port
		internalAddrs = append(internalAddrs, internal)
		hostAddrs = append(hostAddrs, host)
		addressMap[internal] = host
	}
	createArgs := []string{"exec", names[0], "valkey-cli", "--cluster", "create"}
	createArgs = append(createArgs, internalAddrs...)
	createArgs = append(createArgs, "--cluster-replicas", "0", "--cluster-yes")
	if out, createErr := dockerpg.CLI(createArgs...); createErr != nil || !strings.Contains(out, "All 16384 slots covered") {
		t.Fatalf("create Valkey cluster: %v\n%s", createErr, out)
	}
	waitDockerCommand(t, 30*time.Second, func() bool {
		for _, name := range names {
			out, infoErr := dockerpg.CLI("exec", name, "valkey-cli", "cluster", "info")
			if infoErr != nil || !strings.Contains(out, "cluster_state:ok") {
				return false
			}
		}
		return true
	}, "Valkey cluster to become healthy")
	dialer := func(ctx context.Context, network, addr string) (net.Conn, error) {
		if mapped := addressMap[addr]; mapped != "" {
			addr = mapped
		}
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, addr)
	}
	client, err := newUniversalClient(context.Background(), Config{
		Mode: ModeCluster, Addrs: hostAddrs, DialTimeout: 3 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second,
	}, dialer)
	if err != nil {
		t.Fatalf("connect to Valkey cluster: %v", err)
	}
	defer func() { _ = client.Close() }()
	for key, value := range map[string]string{"{alpha}:key": "a", "{beta}:key": "b", "{gamma}:key": "c"} {
		if setErr := client.Set(context.Background(), key, value, 0).Err(); setErr != nil {
			t.Fatalf("cluster set %s: %v", key, setErr)
		}
		if got, getErr := client.Get(context.Background(), key).Result(); getErr != nil || got != value {
			t.Fatalf("cluster get %s: got=%q err=%v", key, got, getErr)
		}
	}
}

func containerAddress(t testing.TB, name, port string) string {
	t.Helper()
	out, err := dockerpg.CLI("inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}", name)
	if err != nil {
		t.Fatalf("inspect %s: %v\n%s", name, err, out)
	}
	return strings.TrimSpace(out) + ":" + port
}

func waitDockerCommand(t testing.TB, timeout time.Duration, ready func() bool, description string) {
	waitDockerCommandWithDiagnostics(t, timeout, ready, description, nil)
}

func waitDockerCommandWithDiagnostics(t testing.TB, timeout time.Duration, ready func() bool, description string, diagnostics func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ready() {
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	if diagnostics == nil {
		t.Fatalf("timed out waiting for %s", description)
	}
	t.Fatalf("timed out waiting for %s\n%s", description, diagnostics())
}

func sentinelMasterAddress(sentinel, masterName string) (string, error) {
	out, err := dockerpg.CLI("exec", sentinel, "valkey-cli", "-p", "26379", "--raw", "sentinel", "get-master-addr-by-name", masterName)
	if err != nil {
		return "", err
	}
	parts := strings.Fields(out)
	if len(parts) != 2 {
		return "", fmt.Errorf("unexpected master address response %q", strings.TrimSpace(out))
	}
	return net.JoinHostPort(parts[0], parts[1]), nil
}

func sentinelMasterState(sentinel, masterName string) (map[string]string, error) {
	out, err := dockerpg.CLI("exec", sentinel, "valkey-cli", "-p", "26379", "--raw", "sentinel", "master", masterName)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimSpace(out), "\n")
	if len(parts)%2 != 0 {
		return nil, fmt.Errorf("unexpected master state response %q", strings.TrimSpace(out))
	}
	state := make(map[string]string, len(parts)/2)
	for index := 0; index < len(parts); index += 2 {
		state[strings.TrimSpace(parts[index])] = strings.TrimSpace(parts[index+1])
	}
	return state, nil
}

func sentinelDiagnostics(dataNames, sentinelNames []string, masterName string) string {
	var diagnostics strings.Builder
	for _, name := range dataNames {
		role, roleErr := dockerpg.CLI("exec", name, "valkey-cli", "--raw", "role")
		fmt.Fprintf(&diagnostics, "data %s role (err=%v):\n%s\n", name, roleErr, role)
	}
	for _, name := range sentinelNames {
		for _, command := range []string{"master", "sentinels", "replicas"} {
			out, commandErr := dockerpg.CLI("exec", name, "valkey-cli", "-p", "26379", "--raw", "sentinel", command, masterName)
			fmt.Fprintf(&diagnostics, "sentinel %s %s (err=%v):\n%s\n", name, command, commandErr, out)
		}
		logs, logsErr := dockerpg.CLI("logs", "--tail", "80", name)
		fmt.Fprintf(&diagnostics, "sentinel %s logs (err=%v):\n%s\n", name, logsErr, logs)
	}
	return diagnostics.String()
}
