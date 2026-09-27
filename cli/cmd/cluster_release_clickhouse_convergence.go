package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/ssh"
)

// Release ClickHouse convergence applies the clickhouse role to every node,
// one at a time. Before a node changes and again before the next one starts,
// every node must reach Keeper and have no replicated table that is read-only,
// lost its Keeper session, lags by more than clickhouseMaxReplicaDelay, or
// still has parts to fetch.

// clickhouseMaxReplicaDelay is the replication delay in seconds a replica may
// report and still count as caught up.
const clickhouseMaxReplicaDelay = 30

var defaultClickHouseGateTiming = serviceGateTiming{Timeout: 15 * time.Minute, Interval: 5 * time.Second}

// clickhouseReleaseOps reads a ClickHouse node's view of Keeper and its
// replicas; problems is empty when the node is caught up.
type clickhouseReleaseOps interface {
	Problems(ctx context.Context, node *orchestrator.Task) ([]string, error)
}

func describeClickHouseConvergence(out io.Writer, tasks []*orchestrator.Task) {
	fmt.Fprintf(out, "     · ClickHouse, one node at a time, each gated on every node reaching Keeper with its replicas caught up: %s\n", strings.Join(taskHosts(tasks), " -> "))
}

func runClickHouseConvergence(ctx context.Context, c *releaseHostConvergence, tasks []*orchestrator.Task, dryRun bool) error {
	out := c.cmd.OutOrStdout()
	ops := c.clickhouseOps()
	timing := c.serviceTiming(defaultClickHouseGateTiming)
	fmt.Fprintf(out, "\nClickHouse (%d node(s))\n", len(tasks))
	gate := func() []string {
		var problems []string
		for _, node := range tasks {
			nodeProblems, err := ops.Problems(ctx, node)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %v", node.Host, err))
				continue
			}
			for _, problem := range nodeProblems {
				problems = append(problems, node.Host+": "+problem)
			}
		}
		return problems
	}
	for _, task := range tasks {
		fmt.Fprintf(out, "  node on %s\n", task.Host)
		if err := c.convergeGated(ctx, task, dryRun, out, timing, "ClickHouse", gate); err != nil {
			return fmt.Errorf("node on %s: %w", task.Host, err)
		}
	}
	return nil
}

func (c *releaseHostConvergence) clickhouseOps() clickhouseReleaseOps {
	if c.serviceOps != nil && c.serviceOps.clickhouse != nil {
		return c.serviceOps.clickhouse
	}
	return sshClickHouseReleaseOps{c: c}
}

// sshClickHouseReleaseOps queries the node's local server with
// clickhouse-client. The password travels on the SSH session's stdin into the
// client's environment, never on a command line.
type sshClickHouseReleaseOps struct {
	c *releaseHostConvergence
}

const clickhouseKeeperQuery = "SELECT count() FROM system.zookeeper WHERE path = '/'"

var clickhouseLaggingReplicasQuery = fmt.Sprintf(
	"SELECT concat(database, '.', table, ' read_only=', toString(is_readonly), ' session_expired=', toString(is_session_expired), "+
		"' delay=', toString(absolute_delay), 's parts_to_fetch=', toString(inserts_in_queue)) FROM system.replicas "+
		"WHERE is_readonly OR is_session_expired OR absolute_delay > %d OR inserts_in_queue > 0 FORMAT TSVRaw", clickhouseMaxReplicaDelay)

// clickhouseReleaseHealthScript prints "keeper ok" or "keeper unreachable:
// <error>", then one "replica <table state>" line per lagging replica, or
// "replicas unreadable: <error>".
const clickhouseReleaseHealthScript = `IFS= read -r CLICKHOUSE_PASSWORD || true
export CLICKHOUSE_PASSWORD
q() { clickhouse-client --host 127.0.0.1 --port %d --query "$1" 2>&1; }
if out="$(q %s)"; then echo "keeper ok"; else echo "keeper unreachable: $(printf '%%s' "$out" | tr '\n' ' ' | cut -c1-300)"; fi
if out="$(q %s)"; then printf '%%s\n' "$out" | sed '/^$/d; s/^/replica /'; else echo "replicas unreadable: $(printf '%%s' "$out" | tr '\n' ' ' | cut -c1-300)"; fi
`

func (o sshClickHouseReleaseOps) Problems(ctx context.Context, node *orchestrator.Task) ([]string, error) {
	host, ok := o.c.manifest.GetHost(node.Host)
	if !ok {
		return nil, fmt.Errorf("host %s not found in manifest", node.Host)
	}
	port := 9000
	if ch := o.c.manifest.Infrastructure.ClickHouse; ch != nil {
		port = ch.EffectivePort()
	}
	client, err := o.c.pool.Get(sshConfigFor(host))
	if err != nil {
		return nil, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	script := fmt.Sprintf(clickhouseReleaseHealthScript, port, ssh.ShellQuote(clickhouseKeeperQuery), ssh.ShellQuote(clickhouseLaggingReplicasQuery))
	var stdout bytes.Buffer
	if _, err := client.RunStream(probeCtx, script, strings.NewReader(o.c.sharedEnv["CLICKHOUSE_PASSWORD"]+"\n"), &stdout); err != nil {
		return nil, err
	}
	return parseClickHouseReleaseHealth(stdout.String())
}

// parseClickHouseReleaseHealth reads clickhouseReleaseHealthScript's output.
func parseClickHouseReleaseHealth(out string) ([]string, error) {
	var problems []string
	sawKeeper := false
	for line := range strings.SplitSeq(strings.ReplaceAll(out, "\r", ""), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case line == "keeper ok":
			sawKeeper = true
		case strings.HasPrefix(line, "keeper unreachable:"):
			sawKeeper = true
			problems = append(problems, "Keeper "+strings.TrimPrefix(line, "keeper "))
		case strings.HasPrefix(line, "replica "):
			problems = append(problems, "replica "+strings.TrimPrefix(line, "replica ")+" is not caught up")
		case strings.HasPrefix(line, "replicas unreadable:"):
			problems = append(problems, line)
		}
	}
	if !sawKeeper {
		return nil, errors.New("unexpected health report " + fmt.Sprintf("%q", strings.TrimSpace(out)))
	}
	return problems, nil
}
