//go:build schema_verify

package provisioner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const ybRehearsalUsageRows = 200000

// TestYugabyteRelayoutRehearsalThreeNodes rehearses a production relayout end to end on an RF3 cluster of three
// tservers with services left running: a production-shaped Purser database with bulk usage data, a writer inserting as
// the runtime role through every tserver from before prepare until after cutover, and a runtime session held open on a
// non-primary tserver. It runs preflight, prepare, cutover straight from prepare, and finish, and proves that prepare
// interrupts no write, that writes fail only inside the cutover window and succeed again right after it, and that every
// acknowledged write, and only those, reached the relaid database. It is opt-in because it needs three engines and
// several minutes.
func TestYugabyteRelayoutRehearsalThreeNodes(t *testing.T) {
	if os.Getenv("FRAMEWORKS_YUGABYTE_REHEARSAL") != "1" {
		t.Skip("set FRAMEWORKS_YUGABYTE_REHEARSAL=1 (make verify-yugabyte-relayout-rehearsal) to rehearse a three-node relayout")
	}
	requireDocker(t)
	nodes := ybStartThreeNodeCluster(t)
	// The shared-fixture helpers address a container by its name; every rehearsal tserver listens on its hostname.
	t.Setenv("FRAMEWORKS_YUGABYTE_TEST_CONTAINER", nodes[0].NodeName)

	const logical, runtimeRole = "purser", "purser_runtime"
	primary := nodes[0]
	layout := ybRelayoutSource(t, primary, logical, logical, runtimeRole)
	loadStarted := time.Now()
	ybLoadRehearsalUsage(t, primary, logical, ybRehearsalUsageRows)
	t.Logf("yugabyte rehearsal: loaded %d usage rows in %s", ybRehearsalUsageRows, time.Since(loadStarted).Round(time.Second))
	ybNodeApply(t, primary, logical, "GRANT INSERT ON public._migrations TO "+relayoutIdentifier(runtimeRole))

	ctx := context.Background()
	relayout := ybRelayoutEngine(logical, logical, runtimeRole, nodes...)
	// Renewals run through docker exec on three memory-capped nodes; each attempt gets a third of the TTL, which must
	// cover a busy restore on a shared developer machine. Production leases last 15 minutes.
	relayout.LeaseTTL = 2 * time.Minute
	before, err := relayout.CollectEvidence(ctx, logical)
	if err != nil {
		t.Fatalf("collect source evidence: %v", err)
	}
	analyticsGrants := ybAnalyticsGrantCount(t, primary, logical)
	if _, err = relayout.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	var report *RelayoutPreflight
	ybLeased(t, relayout, "preflight", func(ctx context.Context) error {
		var preflightErr error
		report, preflightErr = relayout.Preflight(ctx, layout, runtimeRole, ybCapabilityProbes(logical))
		return preflightErr
	})
	timings := report.Timings
	t.Logf("yugabyte rehearsal: preflight tables=%d data=%dB before the window: schema dump=%s pre=%s post=%s; inside it: compare=%s data dump=%s load=%s checksum=%s checks=%s estimated window=%s",
		report.Tables, report.DumpBytes, timings.SchemaDump, timings.PreData, timings.PostData, timings.Compare,
		timings.DataDump, timings.Data, report.ChecksumDuration, report.WindowChecks, report.EstimatedDowntime())

	writer := ybStartRehearsalWriter(t, nodes, logical, runtimeRole)
	writer.waitForSuccesses(t, 3, time.Minute)
	prepareStarted := time.Now()
	ybLeased(t, relayout, "prepare", func(ctx context.Context) error { return relayout.Prepare(ctx, layout) })
	prepareEnded := time.Now()
	writer.waitForSuccesses(t, writer.successCount()+3, time.Minute)

	// A runtime session open on a non-primary tserver must be ended by the fence inside cutover.
	remote := nodes[2]
	session := exec.Command("docker", "exec", remote.NodeName, "ysqlsh", "-X", "-h", remote.Host, "-U", runtimeRole, "-d", logical, "-c", "SELECT pg_sleep(900)")
	if err = session.Start(); err != nil {
		t.Fatalf("open runtime session on %s: %v", remote.NodeName, err)
	}
	sessionDone := make(chan error, 1)
	go func() { sessionDone <- session.Wait() }()
	t.Cleanup(func() { _ = session.Process.Kill() })
	deadline := time.Now().Add(time.Minute)
	for ybNodeApply(t, remote, "yugabyte", "SELECT count(*) FROM pg_stat_activity WHERE datname = "+relayoutLiteral(logical)+" AND query LIKE '%pg_sleep%'") == "0" {
		if time.Now().After(deadline) {
			t.Fatalf("runtime session on %s never appeared", remote.NodeName)
		}
		time.Sleep(time.Second)
	}

	cutoverStarted := time.Now()
	window := ybLeased(t, relayout, "cutover from prepared", func(ctx context.Context) error {
		return relayout.Cutover(ctx, layout, runtimeRole, ybCapabilityProbes(logical))
	})
	cutoverEnded := time.Now()
	select {
	case <-sessionDone:
	case <-time.After(30 * time.Second):
		t.Fatalf("cutover did not end the runtime session on %s", remote.NodeName)
	}
	writer.waitForSuccesses(t, writer.successCount()+6, time.Minute)
	attempts := writer.stop()

	var prepareFailures, windowFailures, lateFailures int
	var firstFailure, lastFailure time.Time
	acknowledged := map[int]bool{}
	for _, attempt := range attempts {
		if attempt.err == nil {
			acknowledged[attempt.seq] = true
			continue
		}
		switch {
		case attempt.started.After(cutoverEnded):
			lateFailures++
			t.Errorf("write %d started after cutover ended and failed: %v", attempt.seq, attempt.err)
		case attempt.ended.Before(cutoverStarted):
			prepareFailures++
			t.Errorf("write %d outside the cutover window failed (prepare ran %s to %s): %v", attempt.seq,
				prepareStarted.Format(time.RFC3339), prepareEnded.Format(time.RFC3339), attempt.err)
		default:
			windowFailures++
			if firstFailure.IsZero() {
				firstFailure = attempt.started
			}
			lastFailure = attempt.ended
		}
	}
	if windowFailures == 0 {
		t.Fatal("no write failed during cutover; the writer did not observe the window")
	}
	written := map[int]bool{}
	for _, line := range strings.Fields(ybNodeApply(t, primary, logical, "SELECT seq FROM public._migrations WHERE version = 'rehearsal-writer' ORDER BY seq")) {
		seq, convErr := strconv.Atoi(line)
		if convErr != nil {
			t.Fatalf("writer row seq %q: %v", line, convErr)
		}
		written[seq] = true
	}
	for seq := range acknowledged {
		if !written[seq] {
			t.Errorf("acknowledged write %d is missing from the relaid database", seq)
		}
	}
	for seq := range written {
		if !acknowledged[seq] {
			t.Errorf("write %d was reported failed but is in the relaid database", seq)
		}
	}
	t.Logf("yugabyte rehearsal: %d writes acknowledged, %d failed inside the window (first failure to last %s), %d outside it",
		len(acknowledged), windowFailures, lastFailure.Sub(firstFailure).Round(time.Second), prepareFailures+lateFailures)
	t.Logf("yugabyte rehearsal: prepare took %s with services running; cutover window %s, preflight estimated %s",
		prepareEnded.Sub(prepareStarted).Round(time.Second), window.Round(time.Second), report.EstimatedDowntime().Round(time.Second))

	// The writer's rows are the only change since the source evidence was taken.
	ybNodeApply(t, primary, logical, "DELETE FROM public._migrations WHERE version = 'rehearsal-writer'")
	ybRequireRelaidDatabase(t, primary, relayout, layout, before, runtimeRole, analyticsGrants)
	for _, node := range nodes[1:] {
		ybRequireRuntimeConnects(t, node, logical, runtimeRole, true)
	}

	ybLeased(t, relayout, "finish", relayout.Finish)
	if err = relayout.Release(ctx); err != nil {
		t.Fatalf("Release: %v", err)
	}
}

// ybRehearsalWriter inserts one row per attempt as the runtime role, each through a new session on the next tserver,
// the way a service's connection pool reconnects after its sessions are ended.
type ybRehearsalWriter struct {
	mu       sync.Mutex
	attempts []ybWriteAttempt
	done     chan struct{}
	finished chan struct{}
}

type ybWriteAttempt struct {
	seq            int
	started, ended time.Time
	err            error
}

func ybStartRehearsalWriter(t *testing.T, nodes []*SSHYugabyteNode, database, role string) *ybRehearsalWriter {
	t.Helper()
	writer := &ybRehearsalWriter{done: make(chan struct{}), finished: make(chan struct{})}
	go func() {
		defer close(writer.finished)
		for seq := 1; ; seq++ {
			select {
			case <-writer.done:
				return
			default:
			}
			node := nodes[seq%len(nodes)]
			attempt := ybWriteAttempt{seq: seq, started: time.Now()}
			writeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			out, err := exec.CommandContext(writeCtx, "docker", "exec", node.NodeName, "ysqlsh", "-X", "-h", node.Host, "-U", role, "-d", database,
				"-v", "ON_ERROR_STOP=1", "-tAc", fmt.Sprintf("INSERT INTO public._migrations (version, phase, seq, filename, checksum) VALUES ('rehearsal-writer', 'expand', %d, 'writer', 'writer')", seq)).CombinedOutput()
			cancel()
			attempt.ended = time.Now()
			if err != nil {
				attempt.err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
			}
			writer.mu.Lock()
			writer.attempts = append(writer.attempts, attempt)
			writer.mu.Unlock()
			time.Sleep(200 * time.Millisecond)
		}
	}()
	t.Cleanup(func() { writer.stop() })
	return writer
}

func (w *ybRehearsalWriter) successCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	count := 0
	for _, attempt := range w.attempts {
		if attempt.err == nil {
			count++
		}
	}
	return count
}

func (w *ybRehearsalWriter) waitForSuccesses(t *testing.T, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for w.successCount() < want {
		if time.Now().After(deadline) {
			t.Fatalf("writer reached %d acknowledged writes, want %d within %s", w.successCount(), want, timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// stop ends the writer once and returns every attempt it made.
func (w *ybRehearsalWriter) stop() []ybWriteAttempt {
	select {
	case <-w.done:
	default:
		close(w.done)
	}
	<-w.finished
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]ybWriteAttempt(nil), w.attempts...)
}

func ybLoadRehearsalUsage(t *testing.T, node *SSHYugabyteNode, database string, rows int) {
	t.Helper()
	const batch = 25000
	for first := 1; first <= rows; first += batch {
		last := min(first+batch-1, rows)
		ybNodeApply(t, node, database, fmt.Sprintf(`INSERT INTO purser.usage_records
  (tenant_id, cluster_id, usage_type, unit, dimensions, dimension_key, source_id, report_id, usage_value, value_kind, period_start, period_end, granularity)
SELECT gen_random_uuid(), 'cluster-' || (g %% 3), 'egress_gb', 'gb', jsonb_build_object('region', 'eu-' || (g %% 4)),
       md5(g::text) || md5((g + 1)::text), 'source-' || (g %% 16), 'report-' || g, (g %% 1000)::numeric, 'delta',
       now() - make_interval(mins => g * 5), now() - make_interval(mins => g * 5 - 5), 'minute_5'
  FROM generate_series(%d, %d) AS g;`, first, last))
	}
}

// ybStartThreeNodeCluster forms an RF3 cluster across three zones on an isolated Docker network and returns one node per
// tserver, addressed by hostname.
func ybStartThreeNodeCluster(t *testing.T) []*SSHYugabyteNode {
	t.Helper()
	image := infrastructureContractImage(t, "yugabyte")
	base := fmt.Sprintf("fw-sv-yb-rehearsal-%d", time.Now().UnixNano())
	prefix := ""
	for octet := 20; octet < 220 && prefix == ""; octet++ {
		candidate := fmt.Sprintf("172.29.%d", octet)
		if _, err := docker(t, "", "network", "create", "--subnet", candidate+".0/24", base); err == nil {
			prefix = candidate
		}
	}
	if prefix == "" {
		t.Fatal("could not allocate a Docker subnet for the rehearsal cluster")
	}
	var names []string
	t.Cleanup(func() {
		for _, name := range names {
			rmContainer(t, name)
		}
		_, _ = docker(t, "", "network", "rm", base)
	})
	var firstAddress string
	for index := 1; index <= 3; index++ {
		name := fmt.Sprintf("%s-%d", base, index)
		address := fmt.Sprintf("%s.%d", prefix, 10+index)
		// Yugabyte sizes its memory from host RAM rather than the container limit, so each process gets an explicit
		// hard limit and the container a backstop; three unbounded nodes exhaust a developer machine. The tablet
		// replica limit scales with that memory (tablet_replicas_per_gib_limit x 10% of the tserver hard limit), so
		// the per-GiB allowance is raised to the headroom of a production-sized node while enforcement stays on.
		args := []string{
			"run", "-d", "--name", name, "--hostname", name, "--network", base, "--ip", address, "--memory=2g", image,
			"bin/yugabyted", "start", "--background=false", "--advertise_address=" + address,
			fmt.Sprintf("--cloud_location=frameworks.eu.z%d", index),
			"--master_flags=memory_limit_hard_bytes=402653184,tablet_replicas_per_gib_limit=5848",
			"--tserver_flags=yb_enable_read_committed_isolation=true,memory_limit_hard_bytes=1073741824,tablet_replicas_per_gib_limit=5848",
		}
		if index == 1 {
			args = append(args, "--fault_tolerance=zone")
			firstAddress = address
		} else {
			args = append(args, "--join="+firstAddress)
		}
		if out, err := docker(t, "", args...); err != nil {
			t.Fatalf("start %s: %v\n%s", name, err, out)
		}
		names = append(names, name)
		ybWaitRehearsalNode(t, name, "SELECT 1", "1")
	}
	ybWaitRehearsalNode(t, names[0], "SELECT count(*) FROM yb_servers()", "3")
	nodes := make([]*SSHYugabyteNode, len(names))
	for i, name := range names {
		nodes[i] = ybRelayoutNode(name, name)
	}
	return nodes
}

func ybWaitRehearsalNode(t *testing.T, name, query, want string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Minute)
	for {
		out, err := docker(t, "", "exec", name, "ysqlsh", "-X", "-h", name, "-U", "yugabyte", "-d", "yugabyte", "-tAc", query)
		if err == nil && strings.TrimSpace(out) == want {
			return
		}
		if time.Now().After(deadline) {
			logs, _ := docker(t, "", "logs", "--tail", "60", name)
			t.Fatalf("%s did not answer %q with %q: %v\n%s", name, query, want, err, logs)
		}
		time.Sleep(2 * time.Second)
	}
}
