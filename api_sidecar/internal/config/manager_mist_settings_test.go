package config

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"frameworks/api_sidecar/internal/appconfig/appconfigtest"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func reconcileSeed() *ipcpb.ConfigSeed {
	return &ipcpb.ConfigSeed{
		FoghornBalancerBase: "http://foghorn:18008",
		Templates:           []*ipcpb.StreamTemplate{{Def: &ipcpb.StreamDef{Name: "live"}}},
	}
}

// Mist's generic config command copies only the keys it knows, so a value it
// does not know is dropped without an error.
func assertNoDroppedConfigKeys(t *testing.T, updates []map[string]interface{}) {
	t.Helper()
	for _, update := range updates {
		for _, key := range []string{"device_discovery", "bwlimit", "bandwidth"} {
			if _, ok := update[key]; ok {
				t.Fatalf("UpdateConfig carries %q, which Mist's config command drops: %#v", key, update)
			}
		}
	}
}

func TestReconcileDisablesDeviceDiscoveryBeforeSave(t *testing.T) {
	fake := &recordingMistAPI{}
	m := &Manager{mistClient: fake, logger: logging.NewLogger(), lastSeed: reconcileSeed()}

	m.reconcile()

	if !slices.Equal(fake.discoverySets, []bool{false}) {
		t.Fatalf("SetDeviceDiscovery calls = %v, want [false]", fake.discoverySets)
	}
	assertNoDroppedConfigKeys(t, fake.updatedConfigs)
	discovery := slices.Index(fake.calls, "SetDeviceDiscovery")
	lastSave := -1
	for i, call := range fake.calls {
		if call == "Save" {
			lastSave = i
		}
	}
	if discovery < 0 || lastSave <= discovery {
		t.Fatalf("calls = %v, want SetDeviceDiscovery before the reconcile Save", fake.calls)
	}
}

func TestReconcileAppliesConfiguredBandwidthLimit(t *testing.T) {
	appconfigtest.Setenv(t, "HELMSMAN_BW_LIMIT_MBPS", "1000")
	fake := &recordingMistAPI{}
	m := &Manager{mistClient: fake, logger: logging.NewLogger(), lastSeed: reconcileSeed()}

	m.reconcile()

	if !slices.Equal(fake.bandwidthSets, []uint64{125_000_000}) {
		t.Fatalf("SetBandwidthLimit calls = %v, want [125000000] bytes/s for 1000 Mbit/s", fake.bandwidthSets)
	}
	assertNoDroppedConfigKeys(t, fake.updatedConfigs)
}

func TestReconcileLeavesMistBandwidthWithoutConfiguredLimit(t *testing.T) {
	fake := &recordingMistAPI{}
	m := &Manager{mistClient: fake, logger: logging.NewLogger(), lastSeed: reconcileSeed()}

	m.reconcile()

	if len(fake.bandwidthSets) != 0 {
		t.Fatalf("SetBandwidthLimit calls = %v, want none without a configured limit", fake.bandwidthSets)
	}
}

func TestReconcileFailsWhenDeviceDiscoveryNotApplied(t *testing.T) {
	fake := &recordingMistAPI{dedicatedError: errors.New("camera_config response missing")}
	m := &Manager{mistClient: fake, logger: logging.NewLogger(), lastSeed: reconcileSeed()}

	m.reconcile()
	m.mu.Lock()
	if m.retryTimer != nil {
		m.retryTimer.Stop()
	}
	applied := m.lastAppliedSum
	m.mu.Unlock()

	if applied != "" {
		t.Fatal("reconcile recorded the seed as applied although device discovery was not disabled")
	}
	if fake.saveCalls != 0 {
		t.Fatalf("Save called %d times after a failed dedicated setting", fake.saveCalls)
	}
}

func TestRepairConfigDriftReassertsDeviceDiscovery(t *testing.T) {
	for name, configSection := range map[string]map[string]interface{}{
		"absent (Mist default is on)": {},
		"enabled":                     {"device_discovery": true},
	} {
		t.Run(name, func(t *testing.T) {
			configSection["triggers"] = desiredTriggers()
			fake := &recordingMistAPI{backupResult: map[string]interface{}{"config": configSection}}
			m := &Manager{mistClient: fake, logger: logging.NewLogger(), lastSeed: &ipcpb.ConfigSeed{}}

			m.repairConfigDrift()

			if !slices.Equal(fake.discoverySets, []bool{false}) {
				t.Fatalf("SetDeviceDiscovery calls = %v, want [false]", fake.discoverySets)
			}
			if fake.saveCalls != 1 {
				t.Fatalf("Save calls = %d, want 1", fake.saveCalls)
			}

			m.repairConfigDrift()
			if len(fake.discoverySets) != 1 || fake.saveCalls != 1 {
				t.Fatalf("converged config was re-applied: discovery=%v saves=%d", fake.discoverySets, fake.saveCalls)
			}
		})
	}
}

func TestRepairConfigDriftReassertsBandwidthLimit(t *testing.T) {
	appconfigtest.Setenv(t, "HELMSMAN_BW_LIMIT_BYTES_PER_SEC", "125000000")
	fake := &recordingMistAPI{backupResult: map[string]interface{}{
		"config":    map[string]interface{}{"device_discovery": false, "triggers": desiredTriggers()},
		"bandwidth": map[string]interface{}{"exceptions": []interface{}{"::1"}},
	}}
	m := &Manager{mistClient: fake, logger: logging.NewLogger(), lastSeed: &ipcpb.ConfigSeed{}}

	m.repairConfigDrift()

	if !slices.Equal(fake.bandwidthSets, []uint64{125_000_000}) {
		t.Fatalf("SetBandwidthLimit calls = %v, want [125000000]", fake.bandwidthSets)
	}
	m.repairConfigDrift()
	if len(fake.bandwidthSets) != 1 {
		t.Fatalf("converged bandwidth limit was re-applied: %v", fake.bandwidthSets)
	}
}

// The drift loop against Mist's HTTP API: config_backup shows discovery on
// and no bandwidth limit, and the repair must send exactly Mist's dedicated
// commands, then save.
func TestRepairConfigDriftSendsMistDedicatedCommands(t *testing.T) {
	appconfigtest.Setenv(t, "HELMSMAN_BW_LIMIT_BYTES_PER_SEC", "125000000")
	triggers, err := json.Marshal(desiredTriggers())
	if err != nil {
		t.Fatal(err)
	}
	var (
		mu       sync.Mutex
		commands []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := r.URL.Query().Get("command")
		var cmd map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
			t.Errorf("command JSON: %v", err)
		}
		switch {
		case cmd["authorize"] != nil:
			_, _ = w.Write([]byte(`{"authorize":{"status":"OK"}}`))
			return
		case cmd["config_backup"] != nil:
			_, _ = w.Write([]byte(`{"config_backup":{"config":{"device_discovery":true,"triggers":` + string(triggers) + `},"bandwidth":{"exceptions":["::1"]}}}`))
			return
		}
		mu.Lock()
		commands = append(commands, raw)
		mu.Unlock()
		switch {
		case cmd["camera_config"] != nil:
			_, _ = w.Write([]byte(`{"camera_config":{"device_discovery":false}}`))
		case cmd["bandwidth"] != nil:
			_, _ = w.Write([]byte(`{"bandwidth":{"limit":125000000,"exceptions":["::1"]}}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	m := &Manager{
		mistClient: mist.NewClient(logging.NewLogger(), mist.ClientConfig{BaseURL: srv.URL}),
		logger:     logging.NewLogger(),
		lastSeed:   &ipcpb.ConfigSeed{},
	}
	m.repairConfigDrift()

	mu.Lock()
	defer mu.Unlock()
	want := []string{
		`{"camera_config":{"device_discovery":false}}`,
		`{"bandwidth":{"limit":125000000}}`,
		`{"save":true}`,
	}
	if !slices.Equal(commands, want) {
		t.Fatalf("Mist commands = %q, want %q", commands, want)
	}
}
