package config

import (
	"errors"
	"net/url"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"google.golang.org/protobuf/proto"
)

func waitForCondition(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		runtime.Gosched()
	}
}

func waitForAckState(t *testing.T, m *Manager, description string, condition func(*Manager) bool) {
	t.Helper()
	waitForCondition(t, description, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return condition(m)
	})
}

func TestStreamConfigsFromSeedSkipsWildcardInstances(t *testing.T) {
	seed := &ipcpb.ConfigSeed{
		Templates: []*ipcpb.StreamTemplate{
			{Def: &ipcpb.StreamDef{Name: "live", Tags: []string{"live"}}},
			{Def: &ipcpb.StreamDef{Name: "processing", Realtime: true, ProcessControlledRealtime: true, Tags: []string{"processing"}}},
			{Def: &ipcpb.StreamDef{Name: "processing+$", Realtime: true}},
			{Def: &ipcpb.StreamDef{Name: "processing+artifact-hash", Realtime: true}},
			{Def: &ipcpb.StreamDef{Name: "dvr", Tags: []string{"dvr"}}},
			{Def: &ipcpb.StreamDef{Name: "pull", Tags: []string{"pull"}}},
		},
	}

	streams := streamConfigsFromSeed(seed, "http://foghorn:18008", "edge-node-1")

	if _, ok := streams["processing+$"]; ok {
		t.Fatal("processing+$ must not be synced as a configured Mist stream")
	}
	if _, ok := streams["processing+artifact-hash"]; ok {
		t.Fatal("processing+ wildcard instances must not be synced as configured Mist streams")
	}
	if got := streams["processing"]["source"]; got != inertMistSource {
		t.Fatalf("processing source = %v, want %q", got, inertMistSource)
	}
	if got := streams["dvr"]["source"]; got != inertMistSource {
		t.Fatalf("dvr source = %v, want %q", got, inertMistSource)
	}
	if got := streams["dvr"]["realtime"]; got != false {
		t.Fatalf("dvr realtime = %v, want false from seed", got)
	}
	if got := streams["processing"]["process_controlled_realtime"]; got != true {
		t.Fatalf("processing process_controlled_realtime = %v, want true from seed", got)
	}
	// Release Mist builds compile at debug level 3; the processing template
	// pins level 4 so processing-job proc activity is observable in prod.
	if got := streams["processing"]["debug"]; got != 4 {
		t.Fatalf("processing debug = %v, want 4", got)
	}
	if got := streams["dvr"]["DVR"]; got != 120000 {
		t.Fatalf("dvr DVR = %v, want 120000", got)
	}
	if got := streams["dvr"]["bufferTime"]; got != 120000 {
		t.Fatalf("dvr bufferTime = %v, want 120000", got)
	}
	if got := streams["dvr"]["inputtimeout"]; got != 12 {
		t.Fatalf("dvr inputtimeout = %v, want 12", got)
	}
	if got := streams["pull"]["source"]; got != "balance:http://foghorn:18008/source/by-node/edge-node-1" {
		t.Fatalf("pull source = %v", got)
	}
	// Live wildcard source: balance:<foghorn>, identical shape to pull.
	// Foghorn's /source dispatch decides the terminal answer: DTSC when
	// the stream is live anywhere, push:// as the publisher safety net,
	// offline:<reason> when neither applies.
	if got := streams["live"]["source"]; got != "balance:http://foghorn:18008/source/by-node/edge-node-1" {
		t.Fatalf("live source = %v, want balance:http://foghorn:18008/source/by-node/edge-node-1", got)
	}
	if got := streams["live"]["DVR"]; got != 120000 {
		t.Fatalf("live DVR = %v, want 120000", got)
	}
	if got := streams["live"]["resume"]; got != 1 {
		t.Fatalf("live resume = %v, want 1", got)
	}
	if got := streams["live"]["inputtimeout"]; got != 12 {
		t.Fatalf("live inputtimeout = %v, want 12", got)
	}
}

func TestSourceBalancerBasePreservesExistingPathAndQuery(t *testing.T) {
	got := sourceBalancerBase("https://foghorn.internal/base?x=1", "edge/node 1")
	want := "https://foghorn.internal/base/source/by-node/edge%2Fnode%201?x=1"
	if got != want {
		t.Fatalf("sourceBalancerBase = %q, want %q", got, want)
	}
}

func TestSourceBalancerCapabilitySurvivesMistQueryReplacement(t *testing.T) {
	base := "https://foghorn.internal/_frameworks/balancer/v1/edge-node-1/123/signature?discarded=yes"
	raw := sourceBalancerBase(base, "edge-node-1")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse configured balance URL: %v", err)
	}

	// MistInputBalancer replaces the complete configured query with these
	// request arguments. Authentication therefore has to survive in the path.
	u.RawQuery = url.Values{"source": {"live+stream"}}.Encode()
	if got, want := u.EscapedPath(), "/_frameworks/balancer/v1/edge-node-1/123/signature/source/by-node/edge-node-1"; got != want {
		t.Fatalf("Mist-shaped request path = %q, want %q", got, want)
	}
	if u.Query().Get("discarded") != "" || u.Query().Get("source") != "live+stream" {
		t.Fatalf("Mist-shaped request query = %q", u.RawQuery)
	}
}

func TestApplyBalancerCapabilitySkipsFullReconcile(t *testing.T) {
	t.Setenv("NODE_ID", "edge-node-1")
	mist := &recordingMistAPI{}
	manager := &Manager{
		mistClient: mist,
		logger:     logging.NewLogger(),
		lastSeed: &ipcpb.ConfigSeed{
			NodeId: "edge-node-1", FoghornBalancerBase: "https://old.example/cap",
			SeedVersion: 1,
			Templates:   []*ipcpb.StreamTemplate{{Def: &ipcpb.StreamDef{Name: "live"}}},
		},
	}

	manager.applyBalancerCapability(&ipcpb.BalancerCapabilityUpdate{
		NodeId: "edge-node-1", FoghornBalancerBase: "https://new.example/cap", SeedVersion: 2,
	})

	if len(mist.updatedConfigs) != 0 || len(mist.addedProtocols) != 0 {
		t.Fatalf("capability rotation ran full reconcile: configs=%d protocols=%d", len(mist.updatedConfigs), len(mist.addedProtocols))
	}
	if len(mist.addedStreams) != 1 || mist.saveCalls != 1 {
		t.Fatalf("capability rotation streams=%d saves=%d, want 1/1", len(mist.addedStreams), mist.saveCalls)
	}
	if got := mist.addedStreams[0]["live"]["source"]; got != "balance:https://new.example/cap/source/by-node/edge-node-1" {
		t.Fatalf("refreshed live source = %v", got)
	}
}

func TestApplySeedRejectsDowngradeWithoutOverwritingLastGoodState(t *testing.T) {
	t.Setenv("HELMSMAN_STATE_DIR", t.TempDir())
	current := &ipcpb.ConfigSeed{
		NodeId: "edge-node-1", SeedVersion: 42,
		FoghornBalancerBase: "https://current.example/cap",
	}
	if err := persistConfigSeed(current); err != nil {
		t.Fatal(err)
	}
	mist := &recordingMistAPI{}
	m := &Manager{mistClient: mist, logger: logging.NewLogger(), lastSeed: current}
	m.applySeed(&ipcpb.ConfigSeed{
		NodeId: "edge-node-1", SeedVersion: 1,
		FoghornBalancerBase: "https://stale.example/cap",
	}, nil)

	persisted, err := loadPersistedConfigSeed()
	if err != nil {
		t.Fatal(err)
	}
	if m.lastSeed.GetSeedVersion() != 42 || persisted.GetSeedVersion() != 42 || persisted.GetFoghornBalancerBase() != "https://current.example/cap" {
		t.Fatalf("stale seed replaced last-good state: memory=%+v disk=%+v", m.lastSeed, persisted)
	}
	if len(mist.updatedConfigs) != 0 || len(mist.addedStreams) != 0 || mist.saveCalls != 0 {
		t.Fatalf("stale seed reached Mist: configs=%d streams=%d saves=%d", len(mist.updatedConfigs), len(mist.addedStreams), mist.saveCalls)
	}
}

func TestConfigSeedApplyResultFenceAdvancesOnlyAfterDurableAcceptance(t *testing.T) {
	seed := &ipcpb.ConfigSeed{NodeId: "edge-node-1", SeedVersion: 7}
	m := &Manager{logger: logging.NewLogger(), lastSeed: seed}

	m.sendApplyResultLocked(seed, nil, true, func(*ipcpb.ControlMessage) error {
		return errors.New("durable queue unavailable")
	})
	waitForAckState(t, m, "ACK-only retry after failed durable acceptance", func(m *Manager) bool {
		return m.pendingApplyAck != nil && m.ackRetryTimer != nil
	})
	m.mu.Lock()
	if m.lastAckedSeedVer != 0 {
		t.Fatalf("failed delivery advanced apply-result fence to %d", m.lastAckedSeedVer)
	}
	if m.retryTimer != nil {
		t.Fatal("ACK transport failure scheduled a full configuration reconcile")
	}
	m.ackRetryTimer.Stop()
	m.ackRetryTimer = nil
	m.pendingApplyAck = nil
	m.pendingAckSender = nil
	m.mu.Unlock()

	m.sendApplyResultLocked(seed, nil, true, func(*ipcpb.ControlMessage) error { return nil })
	waitForAckState(t, m, "durably accepted apply-result fence", func(m *Manager) bool { return m.lastAckedSeedVer == 7 })
}

func TestConfigSeedApplyResultSameVersionFailureFollowsAcceptedSuccess(t *testing.T) {
	seed := &ipcpb.ConfigSeed{NodeId: "edge-node-1", SeedVersion: 7}
	m := &Manager{logger: logging.NewLogger(), lastSeed: seed}
	var sent []*ipcpb.ConfigSeedApplyResult
	var sentMu sync.Mutex
	sender := func(msg *ipcpb.ControlMessage) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sent = append(sent, proto.Clone(msg.GetConfigSeedApplyResult()).(*ipcpb.ConfigSeedApplyResult))
		return nil
	}

	m.sendApplyResultLocked(seed, []bundleApplyResult{{BundleID: "tenant:one", Version: "revision-1", Success: true}}, true, sender)
	waitForAckState(t, m, "accepted success", func(m *Manager) bool {
		return m.lastAckedSeedSum == applyResultSignature(true, []string{"tenant:one"}, nil, map[string]string{"tenant:one": "revision-1"})
	})
	m.sendApplyResultLocked(seed, []bundleApplyResult{{BundleID: "tenant:one", Success: true}}, false, sender)
	waitForAckState(t, m, "accepted demotion", func(m *Manager) bool {
		return m.lastAckedSeedSum == applyResultSignature(false, nil, []string{"tenant:one"}, nil)
	})
	m.sendApplyResultLocked(seed, []bundleApplyResult{{BundleID: "tenant:one", Success: true}}, false, sender)

	sentMu.Lock()
	defer sentMu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("apply result sends = %d, want success plus one equal-version demotion", len(sent))
	}
	if !sent[0].GetSuccess() || sent[1].GetSuccess() || len(sent[1].GetFailedBundleIds()) != 1 {
		t.Fatalf("unexpected apply result transition: %#v", sent)
	}
	if got := sent[0].GetBundleVersions()["tenant:one"]; got != "revision-1" {
		t.Fatalf("applied bundle revision=%q, want revision-1", got)
	}
}

func TestConfigSeedApplyResultSameVersionRecoveryFollowsAcceptedFailure(t *testing.T) {
	seed := &ipcpb.ConfigSeed{NodeId: "edge-node-1", SeedVersion: 7}
	m := &Manager{logger: logging.NewLogger(), lastSeed: seed}
	var sent []*ipcpb.ConfigSeedApplyResult
	var sentMu sync.Mutex
	sender := func(msg *ipcpb.ControlMessage) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sent = append(sent, proto.Clone(msg.GetConfigSeedApplyResult()).(*ipcpb.ConfigSeedApplyResult))
		return nil
	}

	m.sendApplyResultLocked(seed, []bundleApplyResult{{BundleID: "tenant:one", Success: true}}, false, sender)
	waitForAckState(t, m, "accepted failure", func(m *Manager) bool {
		return m.lastAckedSeedSum == applyResultSignature(false, nil, []string{"tenant:one"}, nil)
	})
	m.sendApplyResultLocked(seed, []bundleApplyResult{{BundleID: "tenant:one", Success: true}}, true, sender)
	waitForAckState(t, m, "accepted recovery", func(m *Manager) bool {
		return m.lastAckedSeedSum == applyResultSignature(true, []string{"tenant:one"}, nil, nil)
	})
	m.sendApplyResultLocked(seed, []bundleApplyResult{{BundleID: "tenant:one", Success: true}}, true, sender)

	sentMu.Lock()
	defer sentMu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("apply result sends = %d, want failure plus one equal-version recovery", len(sent))
	}
	if sent[0].GetSuccess() || !sent[1].GetSuccess() || len(sent[1].GetAppliedBundleIds()) != 1 {
		t.Fatalf("unexpected apply result recovery: %#v", sent)
	}
}

func TestConfigSeedApplyResultSameVersionTracksPerBundleTransitions(t *testing.T) {
	seed := &ipcpb.ConfigSeed{NodeId: "edge-node-1", SeedVersion: 7}
	m := &Manager{logger: logging.NewLogger(), lastSeed: seed}
	var sent []*ipcpb.ConfigSeedApplyResult
	var sentMu sync.Mutex
	sender := func(msg *ipcpb.ControlMessage) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sent = append(sent, proto.Clone(msg.GetConfigSeedApplyResult()).(*ipcpb.ConfigSeedApplyResult))
		return nil
	}

	m.sendApplyResultLocked(seed, []bundleApplyResult{
		{BundleID: "tenant:one", Success: true},
		{BundleID: "tenant:two", Success: false, Err: "two failed"},
	}, true, sender)
	waitForAckState(t, m, "accepted first partial outcome", func(m *Manager) bool {
		return m.lastAckedSeedSum == applyResultSignature(false, []string{"tenant:one"}, []string{"tenant:two"}, nil)
	})
	m.sendApplyResultLocked(seed, []bundleApplyResult{
		{BundleID: "tenant:one", Success: false, Err: "one failed"},
		{BundleID: "tenant:two", Success: true},
	}, true, sender)
	waitForAckState(t, m, "accepted second partial outcome", func(m *Manager) bool {
		return m.lastAckedSeedSum == applyResultSignature(false, []string{"tenant:two"}, []string{"tenant:one"}, nil)
	})
	m.sendApplyResultLocked(seed, []bundleApplyResult{
		{BundleID: "tenant:two", Success: true},
		{BundleID: "tenant:one", Success: false, Err: "same outcome"},
	}, true, sender)

	sentMu.Lock()
	defer sentMu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("apply result sends = %d, want both distinct partial outcomes", len(sent))
	}
	if got := sent[1].GetAppliedBundleIds(); len(got) != 1 || got[0] != "tenant:two" {
		t.Fatalf("second applied set = %v", got)
	}
	if got := sent[1].GetFailedBundleIds(); len(got) != 1 || got[0] != "tenant:one" {
		t.Fatalf("second failed set = %v", got)
	}
}

func TestConfigSeedApplyResultRecoveryCancelsUnacceptedDemotion(t *testing.T) {
	seed := &ipcpb.ConfigSeed{NodeId: "edge-node-1", SeedVersion: 7}
	m := &Manager{logger: logging.NewLogger(), lastSeed: seed}
	success := []bundleApplyResult{{BundleID: "tenant:one", Success: true}}
	m.sendApplyResultLocked(seed, success, true, func(*ipcpb.ControlMessage) error { return nil })
	waitForAckState(t, m, "accepted success", func(m *Manager) bool { return m.lastAckedSeedVer == 7 })
	m.sendApplyResultLocked(seed, success, false, func(*ipcpb.ControlMessage) error {
		return errors.New("durable queue unavailable")
	})
	waitForAckState(t, m, "retained failed demotion", func(m *Manager) bool { return m.pendingApplyAck != nil && m.ackRetryTimer != nil })

	m.sendApplyResultLocked(seed, success, true, func(*ipcpb.ControlMessage) error { return nil })
	waitForAckState(t, m, "stale demotion cancellation", func(m *Manager) bool { return m.pendingApplyAck == nil && m.ackRetryTimer == nil })
}

func TestConfigSeedApplyResultReplacementCannotPersistBeforeInflightOutcome(t *testing.T) {
	m := &Manager{logger: logging.NewLogger()}
	failed := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ConfigSeedApplyResult{
		ConfigSeedApplyResult: &ipcpb.ConfigSeedApplyResult{NodeId: "edge-node-1", SeedVersion: 7, FailedBundleIds: []string{"tenant:one"}},
	}}
	recovered := &ipcpb.ControlMessage{Payload: &ipcpb.ControlMessage_ConfigSeedApplyResult{
		ConfigSeedApplyResult: &ipcpb.ConfigSeedApplyResult{NodeId: "edge-node-1", SeedVersion: 7, AppliedBundleIds: []string{"tenant:one"}, Success: true},
	}}
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	persisted := make(chan bool, 2)
	sender := func(msg *ipcpb.ControlMessage) error {
		if !msg.GetConfigSeedApplyResult().GetSuccess() {
			close(firstEntered)
			<-releaseFirst
		}
		persisted <- msg.GetConfigSeedApplyResult().GetSuccess()
		return nil
	}

	firstDone := make(chan struct{})
	go func() {
		m.queueApplyResult(failed, sender)
		close(firstDone)
	}()
	<-firstEntered
	secondDone := make(chan struct{})
	go func() {
		m.queueApplyResult(recovered, sender)
		close(secondDone)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		m.mu.Lock()
		queued := m.pendingApplyAck == recovered
		m.mu.Unlock()
		if queued {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovered outcome was not queued behind in-flight failure")
		}
		runtime.Gosched()
	}
	close(releaseFirst)
	<-firstDone
	<-secondDone

	if first, second := <-persisted, <-persisted; first || !second {
		t.Fatalf("durable outcome order = [%v %v], want [failure recovery]", first, second)
	}
	waitForAckState(t, m, "accepted recovered outcome", func(m *Manager) bool {
		return m.lastAckedSeedSum == applyResultSignature(true, []string{"tenant:one"}, nil, nil)
	})
}

func TestConfigSeedApplyResultEmptyBundleOutcomeIncludesSuccess(t *testing.T) {
	seed := &ipcpb.ConfigSeed{NodeId: "edge-node-1", SeedVersion: 7}
	m := &Manager{logger: logging.NewLogger(), lastSeed: seed}
	var sent []*ipcpb.ConfigSeedApplyResult
	var sentMu sync.Mutex
	sender := func(msg *ipcpb.ControlMessage) error {
		sentMu.Lock()
		defer sentMu.Unlock()
		sent = append(sent, proto.Clone(msg.GetConfigSeedApplyResult()).(*ipcpb.ConfigSeedApplyResult))
		return nil
	}

	m.sendApplyResultLocked(seed, nil, true, sender)
	waitForAckState(t, m, "accepted empty success", func(m *Manager) bool {
		return m.lastAckedSeedSum == applyResultSignature(true, nil, nil, nil)
	})
	m.sendApplyResultLocked(seed, nil, false, sender)
	waitForAckState(t, m, "accepted empty failure", func(m *Manager) bool {
		return m.lastAckedSeedSum == applyResultSignature(false, nil, nil, nil)
	})

	sentMu.Lock()
	defer sentMu.Unlock()
	if len(sent) != 2 || !sent[0].GetSuccess() || sent[1].GetSuccess() {
		t.Fatalf("empty-bundle outcome transition = %#v, want success then failure", sent)
	}
}

func TestReconcileConfiguresGlobalStreamProcessTrigger(t *testing.T) {
	mist := &recordingMistAPI{}
	manager := &Manager{
		mistClient: mist,
		logger:     logging.NewLogger(),
		lastSeed: &ipcpb.ConfigSeed{
			FoghornBalancerBase: "http://foghorn:18008",
			Templates: []*ipcpb.StreamTemplate{
				{Def: &ipcpb.StreamDef{Name: "live"}},
			},
		},
	}

	manager.reconcile()

	if len(mist.updatedConfigs) == 0 {
		t.Fatal("expected UpdateConfig call")
	}
	triggers, ok := mist.updatedConfigs[0]["triggers"].(map[string]any)
	if !ok {
		t.Fatalf("missing triggers in UpdateConfig: %#v", mist.updatedConfigs[0])
	}
	rawHandlers, ok := triggers["STREAM_PROCESS"].([]any)
	if !ok || len(rawHandlers) != 1 {
		t.Fatalf("STREAM_PROCESS trigger = %#v", triggers["STREAM_PROCESS"])
	}
	handler, ok := rawHandlers[0].(map[string]any)
	if !ok {
		t.Fatalf("STREAM_PROCESS handler = %#v", rawHandlers[0])
	}
	if _, scoped := handler["streams"]; scoped {
		t.Fatalf("STREAM_PROCESS must not be stream-scoped; managed streams use bare names: %#v", handler)
	}
}

func TestReconcileConfiguresPushInputCloseTrigger(t *testing.T) {
	mist := &recordingMistAPI{}
	manager := &Manager{
		mistClient: mist,
		logger:     logging.NewLogger(),
		lastSeed: &ipcpb.ConfigSeed{
			FoghornBalancerBase: "http://foghorn:18008",
			Templates: []*ipcpb.StreamTemplate{
				{Def: &ipcpb.StreamDef{Name: "live"}},
			},
		},
	}

	manager.reconcile()

	if len(mist.updatedConfigs) == 0 {
		t.Fatal("expected UpdateConfig call")
	}
	triggers, ok := mist.updatedConfigs[0]["triggers"].(map[string]any)
	if !ok {
		t.Fatalf("missing triggers in UpdateConfig: %#v", mist.updatedConfigs[0])
	}
	rawHandlers, ok := triggers["PUSH_INPUT_CLOSE"].([]any)
	if !ok || len(rawHandlers) != 1 {
		t.Fatalf("PUSH_INPUT_CLOSE trigger = %#v", triggers["PUSH_INPUT_CLOSE"])
	}
	handler, ok := rawHandlers[0].(map[string]any)
	if !ok {
		t.Fatalf("PUSH_INPUT_CLOSE handler = %#v", rawHandlers[0])
	}
	if got := handler["sync"]; got != false {
		t.Fatalf("PUSH_INPUT_CLOSE must be async, got sync=%v", got)
	}
	if got, _ := handler["handler"].(string); got == "" || got[len(got)-len("/push_input_close"):] != "/push_input_close" {
		t.Fatalf("PUSH_INPUT_CLOSE handler URL = %v", got)
	}
}

func TestReconcileConfiguresPlayRewriteFailureAction(t *testing.T) {
	mist := &recordingMistAPI{}
	manager := &Manager{
		mistClient: mist,
		logger:     logging.NewLogger(),
		lastSeed: &ipcpb.ConfigSeed{
			FoghornBalancerBase: "http://foghorn:18008",
			Templates: []*ipcpb.StreamTemplate{
				{Def: &ipcpb.StreamDef{Name: "live"}},
			},
		},
	}

	manager.reconcile()

	if len(mist.updatedConfigs) == 0 {
		t.Fatal("expected UpdateConfig call")
	}
	triggers, ok := mist.updatedConfigs[0]["triggers"].(map[string]any)
	if !ok {
		t.Fatalf("missing triggers in UpdateConfig: %#v", mist.updatedConfigs[0])
	}
	rawHandlers, ok := triggers["PLAY_REWRITE"].([]any)
	if !ok || len(rawHandlers) != 1 {
		t.Fatalf("PLAY_REWRITE trigger = %#v", triggers["PLAY_REWRITE"])
	}
	handler, ok := rawHandlers[0].(map[string]any)
	if !ok {
		t.Fatalf("PLAY_REWRITE handler = %#v", rawHandlers[0])
	}
	if got := handler["onfail"]; got != "deny" {
		t.Fatalf("PLAY_REWRITE onfail = %v", got)
	}
	if _, hasDefault := handler["default"]; hasDefault {
		t.Fatalf("PLAY_REWRITE must not use a sentinel default: %#v", handler)
	}
}

func TestStaleManagedWildcardStreams(t *testing.T) {
	current := map[string]any{
		"streams": map[string]any{
			"live":                     map[string]any{},
			"processing":               map[string]any{},
			"processing+":              map[string]any{},
			"processing+$":             map[string]any{},
			"processing+artifact-hash": map[string]any{},
			"pull+$":                   map[string]any{},
			"dvr+$":                    map[string]any{},
		},
	}

	got := staleManagedWildcardStreams(current)
	want := []string{"dvr+$", "processing+", "processing+$", "pull+$"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("staleManagedWildcardStreams() = %#v, want %#v", got, want)
	}
}

func TestMissingManagedStreams(t *testing.T) {
	expected := map[string]map[string]any{
		"live":       {},
		"vod":        {},
		"dvr":        {},
		"processing": {},
		"pull":       {},
	}
	current := map[string]any{
		"streams": map[string]any{
			"live": map[string]any{},
			"vod":  map[string]any{},
		},
	}

	got := missingManagedStreams(current, expected)
	want := []string{"dvr", "processing", "pull"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("missingManagedStreams() = %#v, want %#v", got, want)
	}
}

func TestMissingManagedStreamsTreatsEmptyConfigAsMissingAll(t *testing.T) {
	expected := map[string]map[string]any{
		"live": {},
		"vod":  {},
	}

	got := missingManagedStreams(map[string]any{"streams": map[string]any{}}, expected)
	want := []string{"live", "vod"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("missingManagedStreams() = %#v, want %#v", got, want)
	}
}

func TestMissingManagedStreamsDetectsDefinitionDrift(t *testing.T) {
	expected := map[string]map[string]any{
		"live": {"name": "live", "source": "balance:https://new.example/source", "realtime": true},
	}
	current := map[string]any{"streams": map[string]any{
		"live": map[string]any{"name": "live", "source": "balance:https://old.example/source", "realtime": true},
	}}
	if got := missingManagedStreams(current, expected); !reflect.DeepEqual(got, []string{"live"}) {
		t.Fatalf("missingManagedStreams() = %#v, want definition drift", got)
	}
}
