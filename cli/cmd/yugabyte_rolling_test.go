package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
)

// fakeUniverse is a three-node universe whose answers the tests steer. Each node runs one tserver and one master; a
// node's tserver uuid is "ts-<name>" and its master uuid "m-<name>".
type fakeUniverse struct {
	mu      sync.Mutex
	serving map[string]bool
	// deadMasters and noLeader make the master list unhealthy.
	deadMasters map[string]bool
	noLeader    bool
	health      yugabyteHealth
	healthErr   error
	// unsafeCalls answers "not safe" to that many SafeToTakeDown calls before answering safe; negative means never.
	unsafeCalls int
	asked       [][]string
	probes      int
	// tserverDead marks a node's tserver as not alive in the registry.
	tserverDead map[string]bool
	// processes maps host/process to its state; a missing entry runs the installed binary.
	processes map[string]yugabyteProcessState
	// mastersErr makes every master read fail, as when no node reaches the masters.
	mastersErr error
	// finalized records the observer of every FinalizeUpgrade call; finalizeErr is what those calls return.
	finalized   []string
	finalizeErr error
	// catalogMigrations is the installed engine's migration set; empty means a consistent single-major set.
	catalogMigrations []string
}

func (u *fakeUniverse) ProcessState(_ context.Context, host inventory.Host, process string) (yugabyteProcessState, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if state, ok := u.processes[host.Name+"/"+process]; ok {
		return state, nil
	}
	return yugabyteProcessCurrent, nil
}

func (u *fakeUniverse) FinalizeUpgrade(_ context.Context, observer inventory.Host) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.finalized = append(u.finalized, observer.Name)
	return u.finalizeErr
}

func (u *fakeUniverse) CatalogMigrations(_ context.Context, _ inventory.Host) ([]string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if len(u.catalogMigrations) == 0 {
		return []string{"V1__1__first.sql", "V2__2__second.sql"}, nil
	}
	return u.catalogMigrations, nil
}

func (u *fakeUniverse) ServesYSQL(_ context.Context, host inventory.Host) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.serving[host.Name]
}

func (u *fakeUniverse) Health(context.Context, inventory.Host) (yugabyteHealth, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.probes++
	return u.health, u.healthErr
}

func (u *fakeUniverse) Masters(context.Context, inventory.Host) ([]yugabyteMaster, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.probes++
	if u.mastersErr != nil {
		return nil, u.mastersErr
	}
	var masters []yugabyteMaster
	for i, name := range []string{"yb-1", "yb-2", "yb-3"} {
		m := yugabyteMaster{UUID: "m-" + name, Host: name, State: "ALIVE", Role: "FOLLOWER"}
		if u.deadMasters[name] {
			m.State = "NETWORK_ERROR"
		}
		if i == 0 && !u.noLeader {
			m.Role = "LEADER"
		}
		masters = append(masters, m)
	}
	return masters, nil
}

func (u *fakeUniverse) Servers(_ context.Context, _, target inventory.Host) (string, string, bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return "ts-" + target.Name, "m-" + target.Name, !u.tserverDead[target.Name], nil
}

func (u *fakeUniverse) SafeToTakeDown(_ context.Context, _ inventory.Host, uuids []string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.asked = append(u.asked, uuids)
	if u.unsafeCalls != 0 {
		if u.unsafeCalls > 0 {
			u.unsafeCalls--
		}
		return errors.New("tablet t1 would lose its leader")
	}
	return nil
}

func (u *fakeUniverse) roll(hosts ...string) *yugabyteRoll {
	roll := &yugabyteRoll{enabled: true, out: io.Discard, u: u, timeout: time.Second, interval: time.Millisecond}
	roll.init()
	for _, name := range hosts {
		roll.hosts = append(roll.hosts, inventory.Host{Name: name})
		roll.serving[name] = u.serving[name]
	}
	return roll
}

// recover brings name back as a completed change does: it serves YSQL and the masters count its servers as alive.
func (u *fakeUniverse) recover(name string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.serving[name] = true
	delete(u.tserverDead, name)
	delete(u.deadMasters, name)
}

func servingAll() map[string]bool { return map[string]bool{"yb-1": true, "yb-2": true, "yb-3": true} }

func ybTask(name string) *orchestrator.Task {
	return &orchestrator.Task{Type: "yugabyte", Name: name, Host: name}
}

func ybBatch(names ...string) []*orchestrator.Task {
	var batch []*orchestrator.Task
	for _, name := range names {
		batch = append(batch, ybTask(name))
	}
	return batch
}

// runChanging runs a provision that applies a change to the node, calling the gate first as provisionTask does.
func runChanging(roll *yugabyteRoll, name string, apply func() error) error {
	_, err := roll.run(context.Background(), ybTask(name), inventory.Host{Name: name}, func(change func() error) (*taskProvisionOutcome, error) {
		if change != nil {
			if err := change(); err != nil {
				return nil, err
			}
		}
		if apply != nil {
			if err := apply(); err != nil {
				return nil, err
			}
		}
		return &taskProvisionOutcome{}, nil
	})
	return err
}

func TestYugabyteRollChangesNodesOneAtATimeRepairsFirst(t *testing.T) {
	universe := &fakeUniverse{serving: map[string]bool{"yb-1": true, "yb-2": false, "yb-3": true}}
	roll := universe.roll("yb-1", "yb-2", "yb-3")
	roll.beginBatch(ybBatch("yb-3", "yb-1", "yb-2"))
	var (
		active, maxActive atomic.Int32
		orderMu           sync.Mutex
		order             []string
		wg                sync.WaitGroup
	)
	for _, name := range []string{"yb-3", "yb-1", "yb-2"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			err := runChanging(roll, name, func() error {
				now := active.Add(1)
				for {
					prev := maxActive.Load()
					if now <= prev || maxActive.CompareAndSwap(prev, now) {
						break
					}
				}
				orderMu.Lock()
				order = append(order, name)
				orderMu.Unlock()
				time.Sleep(5 * time.Millisecond)
				// The repaired node serves again once its change is applied.
				universe.mu.Lock()
				universe.serving[name] = true
				universe.mu.Unlock()
				active.Add(-1)
				return nil
			})
			if err != nil {
				t.Errorf("run %s: %v", name, err)
			}
		}(name)
	}
	wg.Wait()
	if maxActive.Load() != 1 {
		t.Fatalf("%d nodes changed at once, want 1", maxActive.Load())
	}
	if got := strings.Join(order, ","); got != "yb-2,yb-1,yb-3" {
		t.Fatalf("order = %s, want the down node first, then serving nodes by name", got)
	}
}

func TestYugabyteRollStopsChangingNodesAfterAFailure(t *testing.T) {
	universe := &fakeUniverse{serving: servingAll()}
	roll := universe.roll("yb-1", "yb-2", "yb-3")
	roll.beginBatch(ybBatch("yb-1", "yb-2", "yb-3"))
	broken := errors.New("yb-master did not come back")
	if err := runChanging(roll, "yb-1", func() error { return broken }); !errors.Is(err, broken) {
		t.Fatalf("first node = %v, want its provision error", err)
	}
	applied := false
	err := runChanging(roll, "yb-2", func() error { applied = true; return nil })
	if err == nil || applied || !strings.Contains(err.Error(), "yb-1 failed") {
		t.Fatalf("second node err=%v applied=%v, want a refusal naming the failed node before any change", err, applied)
	}
	// A node with nothing to change never reaches the gate, so it still completes.
	if _, err := roll.run(context.Background(), ybTask("yb-3"), inventory.Host{Name: "yb-3"}, func(func() error) (*taskProvisionOutcome, error) {
		return &taskProvisionOutcome{}, nil
	}); err != nil {
		t.Fatalf("no-op node refused after an earlier failure: %v", err)
	}
}

func TestYugabyteRollRefusesAServingNodeWhileAnotherIsDown(t *testing.T) {
	universe := &fakeUniverse{serving: map[string]bool{"yb-1": true, "yb-2": false, "yb-3": true}}
	roll := universe.roll("yb-1", "yb-2", "yb-3")
	applied := false
	err := runChanging(roll, "yb-1", func() error { applied = true; return nil })
	if err == nil || applied || !strings.Contains(err.Error(), "yb-2") {
		t.Fatalf("err=%v applied=%v, want a refusal naming the down node before any change", err, applied)
	}
}

func TestYugabyteRollRepairsADownNodeWhileOthersAreDown(t *testing.T) {
	// yb-1 is down entirely: the masters count neither its tserver nor its master as alive.
	universe := &fakeUniverse{
		serving:     map[string]bool{"yb-1": false, "yb-2": false, "yb-3": true},
		tserverDead: map[string]bool{"yb-1": true, "yb-2": true},
		deadMasters: map[string]bool{"yb-1": true, "yb-2": true},
	}
	roll := universe.roll("yb-1", "yb-2", "yb-3")
	err := runChanging(roll, "yb-1", func() error { universe.recover("yb-1"); return nil })
	if err != nil {
		t.Fatalf("repairing a down node while yb-2 is still down: %v", err)
	}
	if len(universe.asked) != 0 {
		t.Fatalf("repair asked the masters %v; a down node loses the universe nothing", universe.asked)
	}
}

// TestYugabyteRollGatesANodeWhoseYSQLFailsButWhoseServersRun covers a node that fails a YSQL probe while its tserver
// still holds replicas and its master still votes. With another node down, restarting it could lose quorum, so the
// masters must be asked about exactly its live servers, and a refusal must stop the change.
func TestYugabyteRollGatesANodeWhoseYSQLFailsButWhoseServersRun(t *testing.T) {
	for _, tc := range []struct {
		name        string
		unsafeCalls int
		wantApplied bool
	}{
		{"masters confirm it can go down", 0, true},
		{"masters refuse", -1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			universe := &fakeUniverse{
				serving:     map[string]bool{"yb-1": false, "yb-2": false, "yb-3": true},
				tserverDead: map[string]bool{"yb-2": true},
				deadMasters: map[string]bool{"yb-2": true},
				unsafeCalls: tc.unsafeCalls,
			}
			roll := universe.roll("yb-1", "yb-2", "yb-3")
			roll.timeout = 20 * time.Millisecond
			applied := false
			err := runChanging(roll, "yb-1", func() error { applied = true; universe.recover("yb-1"); return nil })
			if applied != tc.wantApplied || (err == nil) != tc.wantApplied {
				t.Fatalf("applied=%v err=%v, want applied=%v", applied, err, tc.wantApplied)
			}
			if len(universe.asked) == 0 || strings.Join(universe.asked[0], ",") != "ts-yb-1,m-yb-1" {
				t.Fatalf("masters were asked %v, want yb-1's live tserver and master", universe.asked)
			}
		})
	}
}

func TestYugabyteRollAsksOnlyAboutTheServersStillAlive(t *testing.T) {
	// yb-1's tserver is gone but its master still votes; only the master decides the quorum.
	universe := &fakeUniverse{
		serving:     map[string]bool{"yb-1": false, "yb-2": true, "yb-3": true},
		tserverDead: map[string]bool{"yb-1": true},
	}
	roll := universe.roll("yb-1", "yb-2", "yb-3")
	if err := runChanging(roll, "yb-1", func() error { universe.recover("yb-1"); return nil }); err != nil {
		t.Fatalf("repairing yb-1: %v", err)
	}
	if len(universe.asked) == 0 || strings.Join(universe.asked[0], ",") != "m-yb-1" {
		t.Fatalf("masters were asked %v, want only yb-1's live master", universe.asked)
	}
}

// TestYugabyteRollAsksAgainAboutAServerThatRecoversWhileWaiting starts with yb-1's tserver down and its master alive
// while the masters refuse. When the tserver comes back during the wait, the change will stop it too, so the next
// questions must include it.
func TestYugabyteRollAsksAgainAboutAServerThatRecoversWhileWaiting(t *testing.T) {
	universe := &fakeUniverse{
		serving:     map[string]bool{"yb-1": false, "yb-2": true, "yb-3": true},
		tserverDead: map[string]bool{"yb-1": true},
		unsafeCalls: -1,
	}
	roll := universe.roll("yb-1", "yb-2", "yb-3")
	roll.timeout = time.Minute
	go func() {
		for {
			universe.mu.Lock()
			asked := len(universe.asked)
			if asked >= 2 {
				delete(universe.tserverDead, "yb-1")
			}
			if asked >= 4 {
				universe.unsafeCalls = 0
				universe.mu.Unlock()
				return
			}
			universe.mu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()
	if err := runChanging(roll, "yb-1", func() error { universe.recover("yb-1"); return nil }); err != nil {
		t.Fatalf("repairing yb-1: %v", err)
	}
	universe.mu.Lock()
	defer universe.mu.Unlock()
	if first := strings.Join(universe.asked[0], ","); first != "m-yb-1" {
		t.Fatalf("first question = %s, want only the live master", first)
	}
	var aboutYB1 []string
	for _, question := range universe.asked {
		if joined := strings.Join(question, ","); strings.Contains(joined, "yb-1") {
			aboutYB1 = append(aboutYB1, joined)
		}
	}
	if last := aboutYB1[len(aboutYB1)-1]; last != "ts-yb-1,m-yb-1" {
		t.Fatalf("questions about yb-1 = %v, want the last to include the recovered tserver", aboutYB1)
	}
}

// TestYugabyteRollRestartsAStoppedSingleNode covers a single-node universe whose only master is stopped. Nothing can
// observe it, and there is no quorum to protect, so the change must go ahead as accepted downtime.
func TestYugabyteRollRestartsAStoppedSingleNode(t *testing.T) {
	universe := &fakeUniverse{
		serving:     map[string]bool{"yb-1": false},
		deadMasters: map[string]bool{"yb-1": true},
		tserverDead: map[string]bool{"yb-1": true},
		mastersErr:  errors.New("yb-admin: no master reachable"),
	}
	roll := universe.roll("yb-1")
	applied := false
	if err := runChanging(roll, "yb-1", func() error { applied = true; universe.recover("yb-1"); return nil }); err != nil || !applied {
		t.Fatalf("restarting the stopped only node = %v, applied=%v", err, applied)
	}
}

func TestYugabyteRollRefusesANodeWhoseStateNoOneCanRead(t *testing.T) {
	universe := &fakeUniverse{serving: map[string]bool{"yb-1": false, "yb-2": false, "yb-3": false}, mastersErr: errors.New("yb-admin: no master reachable")}
	roll := universe.roll("yb-1", "yb-2", "yb-3")
	applied := false
	err := runChanging(roll, "yb-1", func() error { applied = true; return nil })
	if err == nil || applied || !strings.Contains(err.Error(), "no node could read the masters") {
		t.Fatalf("err=%v applied=%v, want a refusal when the masters cannot be read", err, applied)
	}
}

func TestYugabyteRollAsksAboutBothTheTServerAndTheMaster(t *testing.T) {
	universe := &fakeUniverse{serving: servingAll()}
	roll := universe.roll("yb-1", "yb-2", "yb-3")
	if err := runChanging(roll, "yb-2", nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(universe.asked) == 0 || strings.Join(universe.asked[0], ",") != "ts-yb-2,m-yb-2" {
		t.Fatalf("first safety question = %v, want the node's tserver and master", universe.asked)
	}
}

func TestYugabyteRollRefusesUnlessTheMastersConfirmItIsSafe(t *testing.T) {
	cases := []struct {
		name     string
		universe *fakeUniverse
		want     string
	}{
		{"unsafe to take down", &fakeUniverse{serving: servingAll(), unsafeCalls: -1}, "not safe to take down"},
		{"dead master", &fakeUniverse{serving: servingAll(), deadMasters: map[string]bool{"yb-3": true}}, "NETWORK_ERROR"},
		{"no master leader", &fakeUniverse{serving: servingAll(), noLeader: true}, "0 master leader(s)"},
		{"under-replicated tablets", &fakeUniverse{serving: servingAll(), health: yugabyteHealth{UnderReplicatedTablets: []string{"t1"}}}, "under-replicated"},
		{"leaderless tablets", &fakeUniverse{serving: servingAll(), health: yugabyteHealth{LeaderlessTablets: []string{"t1"}}}, "leaderless"},
		{"health unreadable", &fakeUniverse{serving: servingAll(), healthErr: errors.New("master health check has no dead_nodes field")}, "no dead_nodes field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			roll := tc.universe.roll("yb-1", "yb-2", "yb-3")
			applied := false
			err := runChanging(roll, "yb-1", func() error { applied = true; return nil })
			if err == nil || applied || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v applied=%v, want a refusal mentioning %q before any change", err, applied, tc.want)
			}
		})
	}
}

func TestYugabyteRollWaitsForFollowersToCatchUpBeforeTheNextNode(t *testing.T) {
	// The first two safety answers say no, as while the previous node's replicas catch up; the gate retries.
	universe := &fakeUniverse{serving: servingAll(), unsafeCalls: 2}
	roll := universe.roll("yb-1", "yb-2", "yb-3")
	if err := runChanging(roll, "yb-1", nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(universe.asked) < 3 {
		t.Fatalf("asked %d time(s), want the gate to retry until the masters agree", len(universe.asked))
	}
}

func TestYugabyteRollWaitsForTheChangedNodeToRecover(t *testing.T) {
	universe := &fakeUniverse{serving: servingAll(), tserverDead: map[string]bool{}}
	roll := universe.roll("yb-1", "yb-2", "yb-3")
	recovered := make(chan struct{})
	err := runChanging(roll, "yb-1", func() error {
		universe.mu.Lock()
		universe.serving["yb-1"] = false
		universe.tserverDead["yb-1"] = true
		universe.mu.Unlock()
		// The node comes back a little after the change restarted it.
		go func() {
			time.Sleep(20 * time.Millisecond)
			universe.mu.Lock()
			universe.serving["yb-1"] = true
			universe.tserverDead["yb-1"] = false
			universe.mu.Unlock()
			close(recovered)
		}()
		return nil
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	select {
	case <-recovered:
	default:
		t.Fatal("the roll released the node before it served YSQL with a live tserver")
	}
	// Recovery also asks whether each other node could now go down, which only holds once yb-1 has caught up.
	var peers []string
	for _, uuids := range universe.asked {
		peers = append(peers, uuids[0])
	}
	if got := strings.Join(peers, ","); !strings.Contains(got, "ts-yb-2") || !strings.Contains(got, "ts-yb-3") {
		t.Fatalf("recovery asked about %s, want every other node", got)
	}
}

func TestYugabyteRollDoesNotWaitForANodeThatHadNothingToChange(t *testing.T) {
	universe := &fakeUniverse{serving: servingAll(), health: yugabyteHealth{UnderReplicatedTablets: []string{"t1"}}}
	roll := universe.roll("yb-1", "yb-2", "yb-3")
	if _, err := roll.run(context.Background(), ybTask("yb-1"), inventory.Host{Name: "yb-1"}, func(func() error) (*taskProvisionOutcome, error) {
		return &taskProvisionOutcome{}, nil
	}); err != nil {
		t.Fatalf("a no-op node was blocked by universe health: %v", err)
	}
	if universe.probes != 0 {
		t.Fatalf("a no-op node probed the universe %d time(s)", universe.probes)
	}
}

func TestYugabyteRollChangesASingleNodeUniverseWithoutPeers(t *testing.T) {
	universe := &fakeUniverse{serving: map[string]bool{"yb-1": true}}
	roll := universe.roll("yb-1")
	if err := runChanging(roll, "yb-1", nil); err != nil {
		t.Fatalf("single-node universe refused: %v", err)
	}
	if len(universe.asked) != 0 {
		t.Fatalf("single-node universe asked the masters %v; there is no peer to keep quorum", universe.asked)
	}
}

func TestYugabyteRollStopsWaitingForItsTurnWhenCancelled(t *testing.T) {
	universe := &fakeUniverse{serving: servingAll()}
	roll := universe.roll("yb-1", "yb-2")
	roll.beginBatch(ybBatch("yb-1", "yb-2"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := roll.run(ctx, ybTask("yb-2"), inventory.Host{Name: "yb-2"}, func(func() error) (*taskProvisionOutcome, error) {
			return &taskProvisionOutcome{}, nil
		})
		done <- err
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "stopped before reconciling Yugabyte node yb-2") {
			t.Fatalf("cancelled wait = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("a cancelled node kept waiting for its turn")
	}
}

func TestYugabyteRollLeavesBootstrapAndOtherTasksParallel(t *testing.T) {
	universe := &fakeUniverse{serving: map[string]bool{}}
	roll := universe.roll("yb-1")
	roll.enabled = false
	if _, err := roll.run(context.Background(), ybTask("yb-1"), inventory.Host{Name: "yb-1"}, func(change func() error) (*taskProvisionOutcome, error) {
		if change != nil {
			t.Fatal("a disabled roll handed provision a gate")
		}
		return &taskProvisionOutcome{}, nil
	}); err != nil {
		t.Fatalf("bootstrap run: %v", err)
	}
	roll.enabled = true
	if _, err := roll.run(context.Background(), &orchestrator.Task{Type: "kafka", Host: "k-1"}, inventory.Host{Name: "k-1"}, func(change func() error) (*taskProvisionOutcome, error) {
		if change != nil {
			t.Fatal("a non-Yugabyte task got a gate")
		}
		return &taskProvisionOutcome{}, nil
	}); err != nil {
		t.Fatalf("non-Yugabyte task gated by the roll: %v", err)
	}
	if universe.probes != 0 {
		t.Fatal("roll probed the universe for tasks it does not gate")
	}
}

func TestParseYugabyteNodeStateTreatsUnknownProcessesAsUnreadable(t *testing.T) {
	if node := parseYugabyteNodeState("a", "state=bootstrapped running=unknown"); node.Err == nil {
		t.Fatalf("a node whose processes cannot be checked parsed as %+v, want an error", node)
	}
	if node := parseYugabyteNodeState("a", "state=bootstrapped running=no"); node.Err != nil || node.Running {
		t.Fatalf("a stopped node parsed as %+v", node)
	}
}

func TestYugabyteRollModeRequiresProofBeforeParallelProvisioning(t *testing.T) {
	fresh := func(host string) yugabyteNodeState { return yugabyteNodeState{Host: host, State: "fresh"} }
	boot := func(host string, running bool) yugabyteNodeState {
		return yugabyteNodeState{Host: host, State: "bootstrapped", Running: running}
	}
	cases := []struct {
		name    string
		nodes   []yugabyteNodeState
		quorum  bool
		rolling bool
	}{
		{"new universe", []yugabyteNodeState{fresh("a"), fresh("b"), fresh("c")}, false, false},
		{"failed first bootstrap with running process", []yugabyteNodeState{{Host: "a", State: "configured", Running: true}, fresh("b"), fresh("c")}, false, true},
		{"unmarked live quorum", []yugabyteNodeState{{Host: "a", State: "data", Running: true}, fresh("b"), fresh("c")}, true, true},
		{"leader contradicts stopped probes", []yugabyteNodeState{fresh("a"), fresh("b"), fresh("c")}, true, true},
		{"one node serving", []yugabyteNodeState{fresh("a"), {Host: "b", State: "bootstrapped", Running: true, Serving: true}, fresh("c")}, true, true},
		{"one node unreadable", []yugabyteNodeState{fresh("a"), {Host: "b", Err: errors.New("ssh: connection refused")}, fresh("c")}, false, true},
		{"YSQL down everywhere, masters have a leader", []yugabyteNodeState{boot("a", true), boot("b", true), boot("c", true)}, true, true},
		// Unreadable masters with processes still running are unknown state, not proof quorum is lost.
		{"YSQL down and masters unreadable while processes run", []yugabyteNodeState{boot("a", true), boot("b", true), boot("c", true)}, false, true},
		{"masters unreadable, one node still running", []yugabyteNodeState{boot("a", false), boot("b", true), boot("c", false)}, false, true},
		{"bootstrapped universe fully stopped", []yugabyteNodeState{boot("a", false), boot("b", false)}, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rolling, reason := yugabyteRollMode(tc.nodes, tc.quorum)
			if rolling != tc.rolling || reason == "" {
				t.Fatalf("yugabyteRollMode = %v (%q), want rolling=%v", rolling, reason, tc.rolling)
			}
		})
	}
}

func TestParseYugabyteNodeStateFailsClosedOnUnexpectedOutput(t *testing.T) {
	if node := parseYugabyteNodeState("a", "state=bootstrapped running=yes\n"); node.Err != nil || node.State != "bootstrapped" || !node.Running {
		t.Fatalf("parse = %+v", node)
	}
	for _, out := range []string{"", "state=weird running=no", "state=fresh running=maybe", "command not found"} {
		if node := parseYugabyteNodeState("a", out); node.Err == nil {
			t.Errorf("parseYugabyteNodeState(%q) accepted unexpected output", out)
		}
	}
}

// The payloads below follow the 2025.2.3.0 master handlers: HandleHealthCheck, HandleGetReplicationStatus,
// HandleGetTserverStatus, and yb-admin ListAllMasters.
func TestParseYugabyteHealthCheckRequiresEveryField(t *testing.T) {
	health, err := parseYugabyteHealthCheck(`{"dead_nodes":["dead-uuid"],"most_recent_uptime":12,"under_replicated_tablets":["t1","t2"]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(health.DeadNodes) != 1 || len(health.UnderReplicatedTablets) != 2 {
		t.Fatalf("health = %+v", health)
	}
	for _, out := range []string{
		`{}`,
		`{"dead_nodes":[],"most_recent_uptime":1}`,
		`{"dead_nodes":[],"under_replicated_tablets":[]}`,
		`{"error":"Unable to get cluster config"}`,
		`<h2>Unable to list masters</h2>`,
	} {
		if _, err := parseYugabyteHealthCheck(out); err == nil {
			t.Errorf("parseYugabyteHealthCheck(%q) accepted an incomplete response", out)
		}
	}
}

func TestParseYugabyteLeaderlessTablets(t *testing.T) {
	tablets, err := parseYugabyteLeaderlessTablets(`{"leaderless_tablets":[{"table_uuid":"tbl","tablet_uuid":"t9","reason":"no leader"}]}`)
	if err != nil || len(tablets) != 1 || tablets[0] != "t9" {
		t.Fatalf("tablets = %v, %v", tablets, err)
	}
	for _, out := range []string{`{}`, `{"error":"not leader"}`, `<html>`} {
		if _, err := parseYugabyteLeaderlessTablets(out); err == nil {
			t.Errorf("parseYugabyteLeaderlessTablets(%q) accepted an incomplete response", out)
		}
	}
}

func TestParseYugabyteMastersAndProblems(t *testing.T) {
	out := "Master UUID                      \t RPC Host/Port        \t State    \t Role \t Broadcast Host/Port\n" +
		"aaaa \t 10.0.0.1:7100 \t ALIVE \t LEADER \t N/A\n" +
		"bbbb \t 10.0.0.2:7100 \t ALIVE \t FOLLOWER \t N/A\n" +
		"cccc \t 10.0.0.3:7100 \t NETWORK_ERROR \t UNKNOWN_ROLE \t N/A\n"
	masters, err := parseYugabyteMasters(out)
	if err != nil || len(masters) != 3 || masters[0].Host != "10.0.0.1" || masters[2].State != "NETWORK_ERROR" {
		t.Fatalf("masters = %+v, %v", masters, err)
	}
	if problems := yugabyteMasterProblems(masters); len(problems) != 1 || !strings.Contains(problems[0], "cccc") {
		t.Fatalf("problems = %v, want the dead master", problems)
	}
	if problems := yugabyteMasterProblems(masters[1:2]); !strings.Contains(strings.Join(problems, ";"), "0 master leader(s)") {
		t.Fatalf("problems = %v, want the missing leader", problems)
	}
	if _, err := parseYugabyteMasters("Master UUID  RPC Host/Port  State  Role\n"); err == nil {
		t.Fatal("an empty master list parsed")
	}
}

func TestYugabyteServerUUIDsMatchTheNodeByAddress(t *testing.T) {
	registry := `{"placement-1":{"10.0.0.1:9000":{"permanent_uuid":"ts-1","status":"ALIVE"},"10.0.0.2:9000":{"permanent_uuid":"ts-2","status":"DEAD"}}}`
	masters := []yugabyteMaster{{UUID: "m-1", Host: "10.0.0.1"}, {UUID: "m-2", Host: "10.0.0.2"}}
	tserver, master, alive, err := yugabyteServerUUIDs(registry, masters, map[string]bool{"10.0.0.2": true, "yb-2": true})
	if err != nil || tserver != "ts-2" || master != "m-2" || alive {
		t.Fatalf("got %q %q alive=%v %v; want ts-2 m-2 not alive", tserver, master, alive, err)
	}
	if tserver, master, alive, err := yugabyteServerUUIDs(registry, masters, map[string]bool{"10.0.0.9": true}); err != nil || tserver != "" || master != "" || alive {
		t.Fatalf("a node that never joined = %q %q alive=%v %v, want no servers and no error", tserver, master, alive, err)
	}
	if _, _, _, err := yugabyteServerUUIDs(registry, masters, map[string]bool{"10.0.0.1": true, "10.0.0.2": true}); err == nil {
		t.Fatal("a node matching two tablet servers resolved to one")
	}
}

func TestYugabyteFinalizeUpgradeWaitsForEveryNodeToServe(t *testing.T) {
	universe := &fakeUniverse{serving: map[string]bool{"yb-1": true, "yb-2": false, "yb-3": true}}
	roll := universe.roll("yb-1", "yb-2", "yb-3")
	roll.timeout = time.Minute
	go func() {
		time.Sleep(20 * time.Millisecond)
		universe.mu.Lock()
		universe.serving["yb-2"] = true
		universe.mu.Unlock()
	}()
	if err := roll.finalizeUpgrade(context.Background()); err != nil {
		t.Fatalf("finalizeUpgrade: %v", err)
	}
	if len(universe.finalized) != 1 || universe.finalized[0] != "yb-1" {
		t.Fatalf("finalized through %v, want exactly once through yb-1", universe.finalized)
	}
}

func TestYugabyteFinalizeUpgradeRefusesAnUnhealthyUniverse(t *testing.T) {
	cases := map[string]*fakeUniverse{
		"node down":             {serving: map[string]bool{"yb-1": true, "yb-2": false, "yb-3": true}},
		"under-replicated":      {serving: servingAll(), health: yugabyteHealth{UnderReplicatedTablets: []string{"t1"}}},
		"master without leader": {serving: servingAll(), noLeader: true},
	}
	for name, universe := range cases {
		t.Run(name, func(t *testing.T) {
			roll := universe.roll("yb-1", "yb-2", "yb-3")
			roll.timeout = 10 * time.Millisecond
			if err := roll.finalizeUpgrade(context.Background()); err == nil {
				t.Fatal("finalizeUpgrade succeeded on an unhealthy universe")
			}
			if len(universe.finalized) != 0 {
				t.Fatalf("finalized through %v on an unhealthy universe", universe.finalized)
			}
		})
	}
}

func TestYugabyteFinalizeUpgradeReportsFailure(t *testing.T) {
	universe := &fakeUniverse{serving: servingAll(), finalizeErr: errors.New("upgrade_ysql failed")}
	roll := universe.roll("yb-1", "yb-2", "yb-3")
	if err := roll.finalizeUpgrade(context.Background()); err == nil || !strings.Contains(err.Error(), "upgrade_ysql failed") {
		t.Fatalf("finalizeUpgrade error = %v, want the finalize failure", err)
	}
}

func TestYugabyteRollingRestartDeadlineCoversBothWaits(t *testing.T) {
	if got, least := yugabyteRollingRestartDeadline(3), 3*2*yugabyteRollRecoveryTimeout; got <= least {
		t.Fatalf("a three-node restart gets %s, want more than %s so each node can wait for permission and for recovery", got, least)
	}
}

// fakeProcessUniverse fails ProcessState for one host, as a node whose process table cannot be read.
type fakeProcessUniverse struct {
	*fakeUniverse
	unreadable string
}

func (u *fakeProcessUniverse) ProcessState(ctx context.Context, host inventory.Host, process string) (yugabyteProcessState, error) {
	if host.Name == u.unreadable {
		return "", errors.New("cannot tell whether " + process + " runs the installed binary")
	}
	return u.fakeUniverse.ProcessState(ctx, host, process)
}

func TestYugabyteRollStaleRestartsOnlyProcessesOffTheInstalledBinary(t *testing.T) {
	universe := &fakeUniverse{serving: servingAll(), processes: map[string]yugabyteProcessState{
		"yb-1/yb-tserver": yugabyteProcessStale,
		"yb-3/yb-tserver": yugabyteProcessStopped,
		"yb-2/yb-master":  yugabyteProcessStale,
	}}
	roll := universe.roll("yb-1", "yb-2", "yb-3")
	roll.setOrder([]string{"yb-1", "yb-2", "yb-3"})
	var restarted []string
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := roll.rollStale(ctx, "yb-tserver", func(_ context.Context, host inventory.Host) error {
		restarted = append(restarted, host.Name)
		universe.mu.Lock()
		universe.processes[host.Name+"/yb-tserver"] = yugabyteProcessCurrent
		universe.mu.Unlock()
		universe.recover(host.Name)
		return nil
	})
	if err != nil || strings.Join(restarted, ",") != "yb-1,yb-3" {
		t.Fatalf("restarted %v (%v), want the stale and the stopped tserver, in order", restarted, err)
	}
	if len(universe.asked) == 0 {
		t.Fatal("tservers restarted without asking the masters whether each node could go down")
	}

	unreadable := &fakeProcessUniverse{fakeUniverse: &fakeUniverse{serving: servingAll()}, unreadable: "yb-2"}
	roll = &yugabyteRoll{enabled: true, out: io.Discard, u: unreadable, timeout: time.Second, interval: time.Millisecond}
	roll.init()
	for _, name := range []string{"yb-1", "yb-2", "yb-3"} {
		roll.hosts = append(roll.hosts, inventory.Host{Name: name})
		roll.serving[name] = true
	}
	restarted = nil
	err = roll.rollStale(context.Background(), "yb-master", func(_ context.Context, host inventory.Host) error {
		restarted = append(restarted, host.Name)
		return nil
	})
	if err == nil || len(restarted) != 0 {
		t.Fatalf("a node whose process state cannot be read = %v, restarted %v; want a refusal", err, restarted)
	}
}

func TestYugabyteUpgradePhasesConsumeIndependentOrders(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(fmt.Sprintf("stopped_tserver=%v", stopped), func(t *testing.T) {
			universe := &fakeUniverse{serving: servingAll(), processes: map[string]yugabyteProcessState{
				"yb-2/yb-master":  yugabyteProcessStale,
				"yb-1/yb-tserver": yugabyteProcessStale,
				"yb-3/yb-tserver": yugabyteProcessStale,
			}}
			if stopped {
				universe.serving["yb-3"] = false
				universe.tserverDead = map[string]bool{"yb-3": true}
				universe.processes["yb-3/yb-tserver"] = yugabyteProcessStopped
			}
			roll := universe.roll("yb-1", "yb-2", "yb-3")
			roll.setOrder([]string{"yb-1", "yb-2", "yb-3"})
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			for _, name := range roll.order {
				err := roll.changeMaster(ctx, inventory.Host{Name: name}, func(gate func() error) error { return gate() })
				if err != nil {
					t.Fatalf("install phase on %s: %v", name, err)
				}
			}
			var steps []string
			for _, process := range []string{"yb-master", "yb-tserver"} {
				err := roll.rollStale(ctx, process, func(_ context.Context, host inventory.Host) error {
					steps = append(steps, process+":"+host.Name)
					universe.processes[host.Name+"/"+process] = yugabyteProcessCurrent
					if process == "yb-tserver" {
						if universe.processes["yb-2/yb-master"] != yugabyteProcessCurrent {
							t.Fatal("tserver restarted before all masters")
						}
						universe.recover(host.Name)
					} else if stopped && universe.serving["yb-3"] {
						t.Fatal("master phase started a stopped tserver")
					}
					return nil
				})
				if err != nil {
					t.Fatalf("%s phase: %v", process, err)
				}
			}
			if len(steps) != 3 || steps[0] != "yb-master:yb-2" {
				t.Fatalf("unexpected phase sequence: %v", steps)
			}
			if err := roll.finalizeUpgrade(ctx); err != nil {
				t.Fatalf("finalize: %v", err)
			}
			for _, process := range []string{"yb-master", "yb-tserver"} {
				if err := roll.rollStale(ctx, process, func(context.Context, inventory.Host) error {
					t.Fatal("completed upgrade restarted a current process")
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestYugabyteMasterPhaseRefusesUnsafeChange(t *testing.T) {
	universe := &fakeUniverse{serving: servingAll(), unsafeCalls: -1}
	roll := universe.roll("yb-1", "yb-2", "yb-3")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := roll.changeMaster(ctx, inventory.Host{Name: "yb-1"}, func(gate func() error) error {
		if err := gate(); err != nil {
			return err
		}
		t.Fatal("unsafe master was changed")
		return nil
	})
	if err == nil {
		t.Fatal("unsafe master accepted")
	}
	for _, asked := range universe.asked {
		if strings.Join(asked, ",") != "m-yb-1" {
			t.Fatalf("master-only phase asked to stop %v", asked)
		}
	}
}

func TestYugabyteProcessStateDetectsUnappliedConfiguration(t *testing.T) {
	sha, err := exec.LookPath("sha256sum")
	if err != nil {
		t.Skip("sha256sum is required for the node-script contract")
	}
	dir := t.TempDir()
	conf, unit, receipt := filepath.Join(dir, "tserver.conf"), filepath.Join(dir, "tserver.service"), filepath.Join(dir, "applied")
	for path, content := range map[string]string{conf: "--ysql_enable_read_committed_isolation=false\n", unit: "unit\n"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := strings.NewReplacer(
		"/opt/yugabyte/conf/tserver.conf", conf,
		"/etc/systemd/system/yb-tserver.service", unit,
		"/var/lib/yugabyte/data/.frameworks-tserver-config-applied", receipt,
	).Replace(fmt.Sprintf(yugabyteProcessStateScript, "yb-tserver"))
	// Isolate systemd and Linux /proc; execute the real hashing and receipt comparison.
	script = "systemctl() { echo 123; }\nstat() { echo 42; }\n" + script
	script = strings.Replace(script, `binary="$(fw_yb_bin "$process")"`, `binary=/test/yb-tserver`, 1)
	assertState := func(want string) {
		t.Helper()
		out, err := exec.Command("bash", "-c", script).CombinedOutput()
		if err != nil || strings.TrimSpace(string(out)) != "state="+want {
			t.Fatalf("state = %s (%v), want %s", out, err, want)
		}
	}
	recordApplied := func() {
		t.Helper()
		out, err := exec.Command(sha, conf, unit).Output()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(receipt, out, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	assertState("stale")
	recordApplied()
	assertState("current")
	for _, path := range []string{conf, unit} {
		if err := os.WriteFile(path, []byte("new configuration\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		assertState("stale")
		assertState("stale") // A new probe cannot consume the deferred restart.
		recordApplied()
		assertState("current")
	}
	if err := os.Remove(conf); err != nil {
		t.Fatal(err)
	}
	// Suppress sha256sum's diagnostic so only the protocol response is asserted.
	script = "exec 2>/dev/null\n" + script
	assertState("unknown")
}

// TestYugabyteUpgradeRestartsEveryMasterBeforeAnyTServer pins the order YugabyteDB requires for an engine upgrade:
// the install pass restarts only masters, then masters left on the old engine, then every tserver, then finalize.
func TestYugabyteUpgradeRestartsEveryMasterBeforeAnyTServer(t *testing.T) {
	source, err := os.ReadFile("cluster_upgrade.go")
	if err != nil {
		t.Fatalf("read cluster_upgrade.go: %v", err)
	}
	text := string(source)
	steps := []string{
		`config.Metadata["restart_scope"] = "master"`,
		`ybRoll.rollStale(ctx, "yb-master", restartOnly("master"))`,
		`ybRoll.rollStale(ctx, "yb-tserver", restartOnly("tserver"))`,
		`ybRoll.finalizeUpgrade(ctx)`,
	}
	at := -1
	for _, step := range steps {
		next := strings.Index(text, step)
		if next < 0 {
			t.Fatalf("cluster upgrade lacks %q", step)
		}
		if step != steps[0] && next <= at {
			t.Fatalf("cluster upgrade runs %q out of order", step)
		}
		if step != steps[0] {
			at = next
		}
	}
	if !strings.Contains(text, `config.Metadata["allow_engine_change"] = true`) {
		t.Fatal("cluster upgrade does not allow the engine change the role otherwise refuses")
	}
}

// yugabyteOverlaidCatalogMigrations models extracting 2026.1 over 2025.2 without removing the old V90 minors.
// Both archives are valid separately; the mixed directory is not.
func yugabyteOverlaidCatalogMigrations() []string {
	names := make([]string, 0, 100)
	for major := 1; major <= 88; major++ {
		names = append(names, fmt.Sprintf("V%d__1000%d__change.sql", major, major))
	}
	return append(names, yugabyteOverlaidCatalogMigrationTail...)
}

var yugabyteOverlaidCatalogMigrationTail = []string{
	"V89__28474__alter_pg_stat_statements_add_rpc_stats.sql",
	"V90__28107__yb_tablet_metadata.sql",
	"V90.1__28565__add_userid_in_yb_ash.sql",
	"V90.2__29260__yb_binary_upgrade_set_next_colocation_id.sql",
	"V90.3__29299__levenshtein_functions.sql",
	"V90.4__28810__yb_ash_with_input_params.sql",
	"V90.5__15667__yb_stat_auto_analyze.sql",
	"V90.6__28160__yb_qpm.sql",
	"V90.7__29194__alter_pg_stat_statements_add_metrics.sql",
	"V91__28565__add_userid_in_yb_ash.sql",
	"V92__29260__yb_binary_upgrade_set_next_colocation_id.sql",
}

func TestValidateYsqlCatalogMigrations(t *testing.T) {
	cleanTarget := make([]string, 0, 92)
	for _, name := range yugabyteOverlaidCatalogMigrations() {
		if !strings.HasPrefix(name, "V90.") {
			cleanTarget = append(cleanTarget, name)
		}
	}
	tests := []struct {
		name      string
		filenames []string
		wantErr   string
	}{
		{
			name:      "target without stale source minors",
			filenames: cleanTarget,
		},
		{
			name:      "majors only",
			filenames: []string{"V1__1__a.sql", "V2__2__b.sql", "V3__3__c.sql"},
		},
		{
			name:      "majors then minors",
			filenames: []string{"V1__1__a.sql", "V2__2__b.sql", "V2.1__3__c.sql", "V2.2__4__d.sql"},
		},
		{
			name:      "files that are not migrations are ignored",
			filenames: []string{"V1__1__a.sql", "README.md", "V2__2__b.sql"},
		},
		{
			name:      "a major after a minor is rejected",
			filenames: yugabyteOverlaidCatalogMigrations(),
			wantErr:   `"V91__28565__add_userid_in_yb_ash.sql" is not exactly one minor version away from 90.7`,
		},
		{
			name:      "a gap between majors is rejected",
			filenames: []string{"V1__1__a.sql", "V3__3__c.sql"},
			wantErr:   `"V3__3__c.sql" is not exactly one major or minor version away from 1.0`,
		},
		{
			name:      "a gap between minors is rejected",
			filenames: []string{"V1__1__a.sql", "V1.1__2__b.sql", "V1.3__3__c.sql"},
			wantErr:   `"V1.3__3__c.sql" is not exactly one minor version away from 1.1`,
		},
		{
			name:      "two files sharing a version are rejected",
			filenames: []string{"V1__1__a.sql", "V1__2__b.sql"},
			wantErr:   "share version 1.0",
		},
		{
			name:      "an empty set is rejected",
			filenames: []string{"README.md"},
			wantErr:   "found no YSQL catalog migrations",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateYsqlCatalogMigrations(tt.filenames)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("validateYsqlCatalogMigrations: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("validateYsqlCatalogMigrations accepted the set, want %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestYugabyteFinalizeUpgradeRefusesAnEngineWhoseCatalogSetIsRejected(t *testing.T) {
	universe := &fakeUniverse{serving: servingAll(), catalogMigrations: yugabyteOverlaidCatalogMigrations()}
	roll := universe.roll("yb-1", "yb-2", "yb-3")
	roll.timeout = time.Minute
	err := roll.finalizeUpgrade(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not exactly one minor version away") {
		t.Fatalf("finalizeUpgrade error = %v, want the catalog migration set refusal", err)
	}
	// Finalizing promotes the AutoFlags before it reaches the catalog, so it must not be attempted at all.
	if len(universe.finalized) != 0 {
		t.Fatalf("finalized through %v despite a catalog set the engine rejects", universe.finalized)
	}
}
