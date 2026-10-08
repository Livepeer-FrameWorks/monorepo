package cmd

import (
	"context"
	"errors"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/cli/pkg/provisioner"
)

// rolloutLog records every operation across nodes in order.
type rolloutLog struct {
	mu        sync.Mutex
	events    []string
	active    int
	maxActive int
	// inspecting/maxInspecting count concurrent prechecks.
	inspecting    int
	maxInspecting int
}

func (l *rolloutLog) add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *rolloutLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

type fakeRolloutOps struct {
	name       string
	log        *rolloutLog
	inspection provisioner.EdgeInspection
	inspectErr error
	applyErr   error
	drainErr   error
	applyHold  time.Duration
	// drainer, when set, runs the real drain loop against a fake Helmsman.
	drainer       *edgeDrainer
	controlDown   bool
	controlErr    error
	soloInCluster bool
	peersErr      error
	pending       []string
	inspectHold   time.Duration
	// controlAfterApply is what the post-apply control stream wait returns.
	controlAfterApply error
}

func (f *fakeRolloutOps) AwaitControlConnected(context.Context) error {
	return f.controlAfterApply
}

func (f *fakeRolloutOps) ControlConnected(context.Context) (bool, error) {
	return !f.controlDown, f.controlErr
}

func (f *fakeRolloutOps) RoutablePeers(context.Context) (int, error) {
	if f.soloInCluster {
		return 0, f.peersErr
	}
	return 1, f.peersErr
}

func (f *fakeRolloutOps) PendingRestarts(context.Context) ([]string, error) {
	return f.pending, nil
}

func (f *fakeRolloutOps) Inspect(context.Context) (provisioner.EdgeInspection, error) {
	f.log.mu.Lock()
	f.log.inspecting++
	f.log.maxInspecting = max(f.log.maxInspecting, f.log.inspecting)
	f.log.events = append(f.log.events, f.name+":inspect")
	f.log.mu.Unlock()
	time.Sleep(f.inspectHold)
	f.log.mu.Lock()
	f.log.inspecting--
	f.log.mu.Unlock()
	return f.inspection, f.inspectErr
}

// prechecksThen asserts that events start with exactly the given prechecks,
// in any order (they run concurrently), followed by rest in order.
func prechecksThen(t *testing.T, events, prechecks, rest []string) {
	t.Helper()
	if len(events) < len(prechecks) {
		t.Fatalf("events = %v, want prechecks %v first", events, prechecks)
	}
	head := slices.Clone(events[:len(prechecks)])
	slices.Sort(head)
	want := slices.Clone(prechecks)
	slices.Sort(want)
	if !slices.Equal(head, want) || !slices.Equal(events[len(prechecks):], rest) {
		t.Fatalf("events = %v, want prechecks %v then %v", events, prechecks, rest)
	}
}

func (f *fakeRolloutOps) DryRun(context.Context) (provisioner.EdgeInspection, error) {
	f.log.add(f.name + ":dryrun")
	return f.inspection, f.inspectErr
}

func (f *fakeRolloutOps) Apply(context.Context) error {
	f.log.mu.Lock()
	f.log.active++
	if f.log.active > f.log.maxActive {
		f.log.maxActive = f.log.active
	}
	f.log.events = append(f.log.events, f.name+":apply")
	f.log.mu.Unlock()
	time.Sleep(f.applyHold)
	f.log.mu.Lock()
	f.log.active--
	f.log.mu.Unlock()
	return f.applyErr
}

func (f *fakeRolloutOps) Drain(context.Context) (func(context.Context) error, error) {
	f.log.add(f.name + ":drain")
	if f.drainer != nil {
		return f.drainer.Drain(context.Background())
	}
	if f.drainErr != nil {
		return nil, f.drainErr
	}
	return func(context.Context) error {
		f.log.add(f.name + ":restore")
		return nil
	}, nil
}

func liveNode(log *rolloutLog, name string, inspection provisioner.EdgeInspection) (edgeRolloutNode, *fakeRolloutOps) {
	ops := &fakeRolloutOps{name: name, log: log, inspection: inspection}
	return edgeRolloutNode{Name: name, Live: true, Ops: ops}, ops
}

var (
	inSync       = provisioner.EdgeInspection{}
	driftSafe    = provisioner.ClassifyEdgeChanges([]string{"Render env file"})
	driftRestart = provisioner.ClassifyEdgeChanges([]string{"Render env file", "Report mistserver restart in check mode"})
)

func eventIndex(events []string, want string) int {
	for i, e := range events {
		if e == want {
			return i
		}
	}
	return -1
}

// Every live node is prechecked before any apply, and only drifted nodes are
// applied.
func TestEdgeRolloutPrechecksAllThenAppliesChangedOnly(t *testing.T) {
	log := &rolloutLog{}
	a, _ := liveNode(log, "a", inSync)
	b, _ := liveNode(log, "b", driftSafe)
	c, _ := liveNode(log, "c", inSync)
	results := runEdgeRollout(context.Background(), io.Discard, []edgeRolloutNode{a, b, c}, 4, false)

	prechecksThen(t, log.snapshot(), []string{"a:inspect", "b:inspect", "c:inspect"}, []string{"b:apply"})
	actions := []edgeRolloutAction{results[0].Action, results[1].Action, results[2].Action}
	if actions[0] != edgeActionSkip || actions[1] != edgeActionApply || actions[2] != edgeActionSkip {
		t.Fatalf("actions = %v", actions)
	}
}

// Fresh nodes install first and may run in parallel; drifted live nodes run
// strictly one at a time afterwards, whatever --parallel says.
func TestEdgeRolloutOrdersFreshParallelThenLiveSerial(t *testing.T) {
	log := &rolloutLog{}
	var nodes []edgeRolloutNode
	for _, name := range []string{"live-1", "live-2", "live-3"} {
		n, ops := liveNode(log, name, driftSafe)
		ops.applyHold = 20 * time.Millisecond
		nodes = append(nodes, n)
	}
	for _, name := range []string{"fresh-1", "fresh-2"} {
		ops := &fakeRolloutOps{name: name, log: log, applyHold: 50 * time.Millisecond}
		nodes = append(nodes, edgeRolloutNode{Name: name, Live: false, Ops: ops})
	}
	results := runEdgeRollout(context.Background(), io.Discard, nodes, 4, false)
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.Name, r.Err)
		}
	}
	events := log.snapshot()
	if eventIndex(events, "fresh-1:inspect") >= 0 || eventIndex(events, "fresh-2:inspect") >= 0 {
		t.Fatalf("fresh nodes must not be prechecked: %v", events)
	}
	lastFresh := max(eventIndex(events, "fresh-1:apply"), eventIndex(events, "fresh-2:apply"))
	for _, live := range []string{"live-1:apply", "live-2:apply", "live-3:apply"} {
		if eventIndex(events, live) < lastFresh {
			t.Fatalf("%s ran before the fresh installs: %v", live, events)
		}
	}
	if eventIndex(events, "live-1:apply") >= eventIndex(events, "live-2:apply") || eventIndex(events, "live-2:apply") >= eventIndex(events, "live-3:apply") {
		t.Fatalf("live applies out of manifest order: %v", events)
	}
	if log.maxActive != 2 {
		t.Fatalf("max concurrent applies = %d, want 2 (both fresh nodes together, live nodes alone)", log.maxActive)
	}
}

// An apply that restarts media is wrapped in drain → apply → restore; one
// that does not is applied without draining.
func TestEdgeRolloutDrainsAroundRestartingApply(t *testing.T) {
	log := &rolloutLog{}
	safe, _ := liveNode(log, "safe", driftSafe)
	restart, _ := liveNode(log, "restart", driftRestart)
	results := runEdgeRollout(context.Background(), io.Discard, []edgeRolloutNode{safe, restart}, 1, false)

	prechecksThen(t, log.snapshot(), []string{"safe:inspect", "restart:inspect"}, []string{"safe:apply", "restart:drain", "restart:apply", "restart:restore"})
	if results[1].Action != edgeActionDrainedApply {
		t.Fatalf("restart node action = %s", results[1].Action)
	}
}

// --no-drain applies a restarting change in place: the only edge of a cluster
// has nowhere to move its sessions, so a drain would wait out its deadline.
func TestEdgeRolloutNoDrainAppliesRestartingChangeInPlace(t *testing.T) {
	log := &rolloutLog{}
	restart, _ := liveNode(log, "restart", driftRestart)
	results := runEdgeRolloutWith(context.Background(), io.Discard, []edgeRolloutNode{restart}, 1, false, true)

	events := log.snapshot()
	want := []string{"restart:inspect", "restart:apply"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("events = %v, want %v", events, want)
	}
	if results[0].Err != nil || results[0].LeftDraining || results[0].Action != edgeRolloutAction("apply-in-place") {
		t.Fatalf("result = %+v", results[0])
	}
}

// The only routable edge of a cluster keeps an always-on stream, so its drain
// can never reach zero sessions: the rollout applies it in place instead of
// waiting out the drain deadline and failing the node.
func TestEdgeRolloutAppliesOnlyRoutableNodeInPlace(t *testing.T) {
	log := &rolloutLog{}
	n, ops := liveNode(log, "solo", driftRestart)
	h := &fakeHelmsman{mode: "normal", sessions: []int{1}}
	ops.drainer = testDrainer(h, edgeDrainDeadline)
	ops.soloInCluster = true
	var out strings.Builder
	results := runEdgeRollout(context.Background(), &out, []edgeRolloutNode{n}, 1, false)

	events := log.snapshot()
	if eventIndex(events, "solo:drain") >= 0 || eventIndex(events, "solo:apply") < 0 {
		t.Fatalf("events = %v, want an apply without a drain", events)
	}
	if results[0].Err != nil || results[0].LeftDraining {
		t.Fatalf("result = %+v, want applied in place", results[0])
	}
	if len(h.posts) != 0 || h.mode != "normal" {
		t.Fatalf("mode posts = %v, mode = %s; the node must never be set draining", h.posts, h.mode)
	}
	if !strings.Contains(out.String(), "only routable node in its cluster") {
		t.Fatalf("output does not say why the node was applied in place: %q", out.String())
	}
}

// Foghorn applies a drain through Helmsman's control stream; with the stream
// down the drain cannot take effect, so the node is applied in place.
func TestEdgeRolloutAppliesInPlaceWhenControlStreamDown(t *testing.T) {
	log := &rolloutLog{}
	n, ops := liveNode(log, "cut-off", driftRestart)
	h := &fakeHelmsman{mode: "normal", sessions: []int{1}}
	ops.drainer = testDrainer(h, edgeDrainDeadline)
	ops.controlDown = true
	var out strings.Builder
	results := runEdgeRollout(context.Background(), &out, []edgeRolloutNode{n}, 1, false)

	events := log.snapshot()
	if eventIndex(events, "cut-off:drain") >= 0 || eventIndex(events, "cut-off:apply") < 0 {
		t.Fatalf("events = %v, want an apply without a drain", events)
	}
	if results[0].Err != nil || results[0].LeftDraining {
		t.Fatalf("result = %+v, want applied in place", results[0])
	}
	if !strings.Contains(out.String(), "control stream to Foghorn is down") {
		t.Fatalf("output does not say why the node was applied in place: %q", out.String())
	}
}

// When the CLI cannot tell whether another node can take the sessions, it
// does not start a drain that may never finish.
func TestEdgeRolloutAppliesInPlaceWhenRoutabilityUnknown(t *testing.T) {
	log := &rolloutLog{}
	n, ops := liveNode(log, "unknown", driftRestart)
	ops.peersErr = errors.New("cluster nodes requires a platform context")
	var out strings.Builder
	results := runEdgeRollout(context.Background(), &out, []edgeRolloutNode{n}, 1, false)

	if events := log.snapshot(); eventIndex(events, "unknown:drain") >= 0 || eventIndex(events, "unknown:apply") < 0 {
		t.Fatalf("events = %v, want an apply without a drain", events)
	}
	if results[0].Err != nil || !strings.Contains(out.String(), "cannot determine whether another node") {
		t.Fatalf("result = %+v output = %q", results[0], out.String())
	}
}

// A node whose Helmsman cannot reach Foghorn after the apply is not
// provisioned, whatever HTTPS says: fresh installs, plain applies and
// drained applies all fail, and a drained node stays draining.
func TestEdgeRolloutFailsNodeWhoseControlStreamStaysDown(t *testing.T) {
	down := errors.New("the Helmsman control stream to Foghorn is not connected after 1m30s (disconnected)")
	log := &rolloutLog{}
	safe, safeOps := liveNode(log, "safe", driftSafe)
	safeOps.controlAfterApply = down
	drained, drainedOps := liveNode(log, "drained", driftRestart)
	drainedOps.controlAfterApply = down
	freshOps := &fakeRolloutOps{name: "fresh", log: log, controlAfterApply: down}
	fresh := edgeRolloutNode{Name: "fresh", Ops: freshOps}
	results := runEdgeRollout(context.Background(), io.Discard, []edgeRolloutNode{safe, drained, fresh}, 1, false)

	for _, r := range results {
		if r.Err == nil || !strings.Contains(r.Err.Error(), "control stream to Foghorn is not connected") {
			t.Fatalf("%s: err = %v, want the control stream failure", r.Name, r.Err)
		}
	}
	if !results[1].LeftDraining || eventIndex(log.snapshot(), "drained:restore") >= 0 {
		t.Fatalf("drained node = %+v events = %v; it must stay draining", results[1], log.snapshot())
	}
	var summary strings.Builder
	if err := summarizeEdgeRollout(&summary, results, false); err == nil {
		t.Fatalf("summary reported success: %q", summary.String())
	}
}

// --dry-run reports the decision the apply would make for a media-restarting
// change, using only reads: the only routable node would apply in place.
func TestEdgeRolloutDryRunReportsInPlaceDecision(t *testing.T) {
	log := &rolloutLog{}
	solo, soloOps := liveNode(log, "solo", driftRestart)
	soloOps.soloInCluster = true
	var out strings.Builder
	results := runEdgeRollout(context.Background(), &out, []edgeRolloutNode{solo}, 1, true)

	if events := log.snapshot(); strings.Join(events, ",") != "solo:dryrun" {
		t.Fatalf("dry-run events = %v, want only the check", events)
	}
	if results[0].Action != edgeRolloutAction("apply-in-place") || !strings.Contains(out.String(), "only routable node in its cluster") {
		t.Fatalf("dry-run action = %s output = %q, want apply-in-place with the reason", results[0].Action, out.String())
	}
}

// Live nodes are prechecked concurrently; their applies stay serial.
func TestEdgeRolloutPrechecksLiveNodesConcurrently(t *testing.T) {
	log := &rolloutLog{}
	var nodes []edgeRolloutNode
	for _, name := range []string{"a", "b", "c", "d"} {
		n, ops := liveNode(log, name, driftSafe)
		ops.inspectHold = 30 * time.Millisecond
		ops.applyHold = 5 * time.Millisecond
		nodes = append(nodes, n)
	}
	results := runEdgeRollout(context.Background(), io.Discard, nodes, 1, false)
	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("%s: %v", r.Name, r.Err)
		}
	}
	if log.maxInspecting < 2 {
		t.Fatalf("max concurrent prechecks = %d, want the live nodes checked together", log.maxInspecting)
	}
	if log.maxActive != 1 {
		t.Fatalf("max concurrent applies = %d, want 1", log.maxActive)
	}
	prechecksThen(t, log.snapshot(), []string{"a:inspect", "b:inspect", "c:inspect", "d:inspect"}, []string{"a:apply", "b:apply", "c:apply", "d:apply"})
}

func TestEdgeRolloutDrainFailureSkipsApply(t *testing.T) {
	log := &rolloutLog{}
	n, ops := liveNode(log, "busy", driftRestart)
	ops.drainErr = errors.New("3 session(s) still active")
	results := runEdgeRollout(context.Background(), io.Discard, []edgeRolloutNode{n}, 1, false)
	if eventIndex(log.snapshot(), "busy:apply") >= 0 {
		t.Fatalf("apply ran after a failed drain: %v", log.snapshot())
	}
	if results[0].Err == nil || results[0].LeftDraining {
		t.Fatalf("result = %+v, want drain error and not left draining", results[0])
	}
}

func TestEdgeRolloutFailedApplyLeavesNodeDraining(t *testing.T) {
	log := &rolloutLog{}
	n, ops := liveNode(log, "broken", driftRestart)
	ops.applyErr = errors.New("edge role apply failed")
	results := runEdgeRollout(context.Background(), io.Discard, []edgeRolloutNode{n}, 1, false)
	if eventIndex(log.snapshot(), "broken:restore") >= 0 {
		t.Fatalf("restore ran after a failed apply: %v", log.snapshot())
	}
	if !results[0].LeftDraining || results[0].Err == nil {
		t.Fatalf("result = %+v, want error and left draining", results[0])
	}
}

// A precheck error never falls through to an undrained apply.
func TestEdgeRolloutPrecheckFailureDoesNotApply(t *testing.T) {
	log := &rolloutLog{}
	n, ops := liveNode(log, "unknown", inSync)
	ops.inspectErr = errors.New("ansible check emitted no PLAY RECAP")
	results := runEdgeRollout(context.Background(), io.Discard, []edgeRolloutNode{n}, 1, false)
	if eventIndex(log.snapshot(), "unknown:apply") >= 0 || results[0].Err == nil {
		t.Fatalf("events = %v result = %+v", log.snapshot(), results[0])
	}
}

// --dry-run checks every node (fresh ones too) and never applies or drains.
func TestEdgeRolloutDryRunDoesNotMutate(t *testing.T) {
	log := &rolloutLog{}
	live, _ := liveNode(log, "live", driftRestart)
	fresh := edgeRolloutNode{Name: "fresh", Ops: &fakeRolloutOps{name: "fresh", log: log, inspection: driftSafe}}
	var out strings.Builder
	results := runEdgeRollout(context.Background(), &out, []edgeRolloutNode{live, fresh}, 4, true)
	events := log.snapshot()
	if strings.Join(events, ",") != "live:dryrun,fresh:dryrun" {
		t.Fatalf("dry-run events = %v, want only dry-run checks", events)
	}
	if results[0].Action != edgeActionDrainedApply || results[1].Action != edgeActionInstall {
		t.Fatalf("dry-run actions = %s/%s", results[0].Action, results[1].Action)
	}
	if !strings.Contains(out.String(), "[live] plan: drain+apply") || !strings.Contains(out.String(), "Report mistserver restart in check mode") {
		t.Fatalf("dry-run output missing the plan: %q", out.String())
	}
	var summary strings.Builder
	if err := summarizeEdgeRollout(&summary, results, true); err != nil {
		t.Fatalf("summary: %v", err)
	}
	if !strings.Contains(summary.String(), "would drain+apply") {
		t.Fatalf("summary = %q", summary.String())
	}
}

// fakeHelmsman answers the curl calls the drainer makes.
type fakeHelmsman struct {
	mu        sync.Mutex
	mode      string
	sessions  []int // successive stream_viewers totals
	posts     []string
	failPosts int
	// metricsErrs fails that many /metrics reads before answering.
	metricsErrs int
	// control are successive helmsman_control_stream_connected values;
	// nil leaves the gauge out.
	control []int
}

func (h *fakeHelmsman) run(_ context.Context, args []string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	url := args[len(args)-1]
	switch {
	case strings.HasSuffix(url, "/node/mode") && containsArg(args, "POST"):
		if h.failPosts > 0 {
			h.failPosts--
			return "", errors.New("503 control stream disconnected")
		}
		body := args[indexOfArg(args, "-d")+1]
		switch {
		case strings.Contains(body, `"draining"`):
			h.mode = "draining"
		case strings.Contains(body, `"normal"`):
			h.mode = "normal"
		}
		h.posts = append(h.posts, h.mode)
		return `{"status":"requested"}`, nil
	case strings.HasSuffix(url, "/node/mode"):
		return `{"mode":"` + h.mode + `","node_id":"edge-1"}`, nil
	case strings.HasSuffix(url, "/metrics"):
		if h.metricsErrs > 0 {
			h.metricsErrs--
			return "", errors.New("connection refused")
		}
		control := ""
		if len(h.control) > 0 {
			control = "helmsman_control_stream_connected " + strconv.Itoa(h.control[0]) + "\n"
			if len(h.control) > 1 {
				h.control = h.control[1:]
			}
		}
		n := 0
		if len(h.sessions) > 0 {
			n = h.sessions[0]
			if len(h.sessions) > 1 {
				h.sessions = h.sessions[1:]
			}
		}
		if n == 0 {
			return control + "# HELP stream_viewers Number of viewers per stream\n# TYPE stream_viewers gauge\n", nil
		}
		return control + "# TYPE stream_viewers gauge\nstream_viewers{stream=\"live+a\"} " + strconv.Itoa(n-1) + "\nstream_viewers{stream=\"live+b\"} 1\nstream_viewers_other 9\n", nil
	}
	return "", errors.New("unexpected call " + strings.Join(args, " "))
}

func containsArg(args []string, want string) bool { return indexOfArg(args, want) >= 0 }

func indexOfArg(args []string, want string) int {
	for i, a := range args {
		if a == want {
			return i
		}
	}
	return -1
}

func testDrainer(h *fakeHelmsman, deadline time.Duration) *edgeDrainer {
	d := newEdgeDrainer(io.Discard, "edge-1", h.run)
	clock := time.Unix(0, 0)
	d.now = func() time.Time { return clock }
	d.sleep = func(_ context.Context, dur time.Duration) error {
		clock = clock.Add(dur)
		return nil
	}
	d.deadline = deadline
	return d
}

func TestEdgeDrainerDrainsWaitsAndRestores(t *testing.T) {
	h := &fakeHelmsman{mode: "normal", sessions: []int{3, 1, 0}}
	restore, err := testDrainer(h, time.Hour).Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if h.mode != "draining" || len(h.sessions) != 1 {
		t.Fatalf("mode=%s remaining session polls=%v; drain must wait for zero sessions", h.mode, h.sessions)
	}
	h.failPosts = 2 // Helmsman reconnecting to Foghorn after the apply
	if err := restore(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if h.mode != "normal" {
		t.Fatalf("mode after restore = %s, want normal", h.mode)
	}
}

func TestEdgeDrainerDeadlineRestoresAndFails(t *testing.T) {
	h := &fakeHelmsman{mode: "normal", sessions: []int{2}}
	_, err := testDrainer(h, time.Minute).Drain(context.Background())
	if err == nil || !strings.Contains(err.Error(), "2 session(s) still active") {
		t.Fatalf("Drain err = %v, want sessions still active", err)
	}
	if h.mode != "normal" {
		t.Fatalf("mode = %s; a drain that cannot finish must restore normal", h.mode)
	}
}

// A drain in a multi-node cluster is bounded to minutes, prints one progress
// line per poll (a failed session read included), and on its deadline
// restores the mode and fails the node.
func TestEdgeDrainerBoundedDeadlineReportsEveryPoll(t *testing.T) {
	h := &fakeHelmsman{mode: "normal", sessions: []int{2}, metricsErrs: 1}
	var out strings.Builder
	d := testDrainer(h, edgeDrainDeadline)
	d.w = &out
	if d.deadline > 15*time.Minute {
		t.Fatalf("edge provision drain deadline = %s, want at most 15m", d.deadline)
	}
	_, err := d.Drain(context.Background())
	if err == nil || !strings.Contains(err.Error(), "2 session(s) still active") {
		t.Fatalf("Drain err = %v, want sessions still active", err)
	}
	if h.mode != "normal" {
		t.Fatalf("mode = %s; a drain that cannot finish must restore normal", h.mode)
	}
	polls := int(d.deadline/d.poll) + 1
	var progress []string
	for line := range strings.SplitSeq(out.String(), "\n") {
		if strings.HasPrefix(line, "[edge-1] draining: ") {
			progress = append(progress, line)
		}
	}
	if len(progress) != polls {
		t.Fatalf("got %d progress lines for %d polls:\n%s", len(progress), polls, out.String())
	}
	if !strings.Contains(progress[0], "session count unavailable") || !strings.Contains(progress[1], "2 session(s) remaining") {
		t.Fatalf("progress lines do not report the failed read and the session count: %q", progress[:2])
	}
	if !strings.Contains(progress[len(progress)-1], d.deadline.String()+" elapsed of "+d.deadline.String()) {
		t.Fatalf("last progress line does not show the elapsed time against the deadline: %q", progress[len(progress)-1])
	}
}

func TestEdgeDrainerKeepsOperatorMode(t *testing.T) {
	h := &fakeHelmsman{mode: "maintenance"}
	restore, err := testDrainer(h, time.Hour).Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if err := restore(context.Background()); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(h.posts) != 0 || h.mode != "maintenance" {
		t.Fatalf("posts=%v mode=%s; an operator-set mode must be left alone", h.posts, h.mode)
	}
}

func TestSumStreamViewers(t *testing.T) {
	got, err := sumStreamViewers("# TYPE stream_viewers gauge\nstream_viewers{stream=\"a\"} 2\nstream_viewers{stream=\"b\"} 3\nstream_viewers_total 7\n")
	if err != nil || got != 5 {
		t.Fatalf("sumStreamViewers = %d, %v; want 5", got, err)
	}
	if _, err := sumStreamViewers("stream_viewers{stream=\"a\"} nope\n"); err == nil {
		t.Fatal("malformed sample must error")
	}
}
