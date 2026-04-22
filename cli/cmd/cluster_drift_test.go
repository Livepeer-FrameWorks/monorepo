package cmd

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
)

type stubClusterDetector struct {
	responses map[string]*detect.ServiceState
}

func (s *stubClusterDetector) Detect(_ context.Context, serviceName string) (*detect.ServiceState, error) {
	if state, ok := s.responses[serviceName]; ok {
		return state, nil
	}
	return nil, nil
}

func TestClassifyClusterService_detectErrorIsMissing(t *testing.T) {
	t.Parallel()
	entry := classifyClusterService("host-1", "bridge", "docker", "v1.0.0", nil, errors.New("boom"))
	if entry.Status != driftClusterMissing {
		t.Errorf("status: want missing, got %s", entry.Status)
	}
	if entry.Detail != "boom" {
		t.Errorf("detail: want boom, got %q", entry.Detail)
	}
}

func TestClassifyClusterService_nilStateIsMissing(t *testing.T) {
	t.Parallel()
	entry := classifyClusterService("host-1", "bridge", "docker", "", nil, nil)
	if entry.Status != driftClusterMissing {
		t.Errorf("want missing, got %s", entry.Status)
	}
}

func TestClassifyClusterService_notExistsIsMissing(t *testing.T) {
	t.Parallel()
	entry := classifyClusterService("host-1", "bridge", "docker", "", &detect.ServiceState{Exists: false}, nil)
	if entry.Status != driftClusterMissing {
		t.Errorf("want missing, got %s", entry.Status)
	}
}

func TestClassifyClusterService_existsButNotRunningIsStopped(t *testing.T) {
	t.Parallel()
	state := &detect.ServiceState{Exists: true, Running: false, Mode: "docker", Version: "v1.0.0"}
	entry := classifyClusterService("host-1", "bridge", "docker", "v1.0.0", state, nil)
	if entry.Status != driftClusterStopped {
		t.Errorf("want stopped, got %s", entry.Status)
	}
	if entry.Mode != "docker" || entry.Version != "v1.0.0" {
		t.Errorf("mode/version should still be populated: %+v", entry)
	}
}

func TestClassifyClusterService_wrongMode(t *testing.T) {
	t.Parallel()
	state := &detect.ServiceState{Exists: true, Running: true, Mode: "native", Version: "v1.0.0"}
	entry := classifyClusterService("host-1", "bridge", "docker", "v1.0.0", state, nil)
	if entry.Status != driftClusterWrongMode {
		t.Errorf("want wrong_mode, got %s", entry.Status)
	}
	if entry.Detail != "have native, want docker" {
		t.Errorf("detail: want 'have native, want docker', got %q", entry.Detail)
	}
}

func TestClassifyClusterService_modeUnspecifiedDoesNotTriggerWrongMode(t *testing.T) {
	t.Parallel()
	state := &detect.ServiceState{Exists: true, Running: true, Mode: "docker", Version: "v1.0.0"}
	entry := classifyClusterService("host-1", "bridge", "", "v1.0.0", state, nil)
	if entry.Status != driftClusterOK {
		t.Errorf("want ok (no mode constraint), got %s", entry.Status)
	}
}

func TestClassifyClusterService_wrongVersion(t *testing.T) {
	t.Parallel()
	state := &detect.ServiceState{Exists: true, Running: true, Mode: "docker", Version: "v1.0.0"}
	entry := classifyClusterService("host-1", "bridge", "docker", "v1.1.0", state, nil)
	if entry.Status != driftClusterWrongVersion {
		t.Errorf("want wrong_version, got %s", entry.Status)
	}
	if entry.Detail != "have v1.0.0, want v1.1.0" {
		t.Errorf("detail: want 'have v1.0.0, want v1.1.0', got %q", entry.Detail)
	}
}

func TestClassifyClusterService_availableUnknownSkipsVersionCheck(t *testing.T) {
	t.Parallel()
	state := &detect.ServiceState{Exists: true, Running: true, Mode: "docker", Version: "v1.0.0"}
	entry := classifyClusterService("host-1", "bridge", "docker", "", state, nil)
	if entry.Status != driftClusterOK {
		t.Errorf("want ok (version unknown), got %s", entry.Status)
	}
}

func TestClassifyClusterService_allMatchesIsOK(t *testing.T) {
	t.Parallel()
	state := &detect.ServiceState{Exists: true, Running: true, Mode: "docker", Version: "v1.1.0"}
	entry := classifyClusterService("host-1", "bridge", "docker", "v1.1.0", state, nil)
	if entry.Status != driftClusterOK {
		t.Errorf("want ok, got %s", entry.Status)
	}
}

func TestResolveDesiredVersion_pinnedWins(t *testing.T) {
	t.Parallel()
	got := resolveDesiredVersion("v1.2.3", nil, "bridge")
	if got != "v1.2.3" {
		t.Errorf("want v1.2.3, got %q", got)
	}
}

func TestResolveDesiredVersion_noPinNoManifestIsEmpty(t *testing.T) {
	t.Parallel()
	if got := resolveDesiredVersion("", nil, "bridge"); got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

func TestCountClusterDriftDivergences(t *testing.T) {
	t.Parallel()
	entries := []clusterDriftEntry{
		{Status: driftClusterOK},
		{Status: driftClusterOK},
		{Status: driftClusterStopped},
		{Status: driftClusterWrongMode},
		{Status: driftClusterMissing},
	}
	if got := countClusterDriftDivergences(entries); got != 3 {
		t.Errorf("want 3, got %d", got)
	}
}

func TestRenderClusterDriftText_cleanRun(t *testing.T) {
	t.Parallel()
	rep := clusterDriftReport{
		Cluster:         "central-primary",
		Channel:         "stable",
		PlatformVersion: "v0.2.2",
		Entries: []clusterDriftEntry{
			{Host: "h1", Service: "bridge", Status: driftClusterOK, Mode: "docker", Version: "v0.2.2"},
		},
		Summary: clusterDriftSummary{Total: 1, Divergences: 0},
	}
	var buf bytes.Buffer
	renderClusterDriftText(&buf, rep)
	text := buf.String()
	if !strings.Contains(text, "central-primary") {
		t.Errorf("missing cluster header; got:\n%s", text)
	}
	if !strings.Contains(text, "No drift detected") {
		t.Errorf("missing clean-run footer; got:\n%s", text)
	}
	if !strings.Contains(text, "bridge") || !strings.Contains(text, "v0.2.2") {
		t.Errorf("missing service row; got:\n%s", text)
	}
}

func TestBuildClusterDriftTargets_coversInfrastructure(t *testing.T) {
	t.Parallel()
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"pg-1": {Name: "pg-1"}, "pg-2": {Name: "pg-2"},
			"kf-1": {Name: "kf-1"}, "kc-1": {Name: "kc-1"},
			"zk-1": {Name: "zk-1"},
			"ch-1": {Name: "ch-1"},
			"rd-1": {Name: "rd-1"}, "rd-2": {Name: "rd-2"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{
				Enabled: true, Engine: "yugabyte", Mode: "native", Version: "2.20.0",
				Nodes: []inventory.PostgresNode{{Host: "pg-1"}, {Host: "pg-2"}},
			},
			ClickHouse: &inventory.ClickHouseConfig{Enabled: true, Mode: "native", Version: "24.3", Host: "ch-1"},
			Kafka: &inventory.KafkaConfig{
				Enabled: true, Mode: "native", Version: "3.7.0",
				Brokers:     []inventory.KafkaBroker{{Host: "kf-1"}},
				Controllers: []inventory.KafkaController{{Host: "kc-1"}},
			},
			Zookeeper: &inventory.ZookeeperConfig{
				Enabled: true, Mode: "native", Version: "3.9.0",
				Ensemble: []inventory.ZookeeperNode{{Host: "zk-1"}},
			},
			Redis: &inventory.RedisConfig{
				Enabled: true, Mode: "native", Version: "7.2",
				Instances: []inventory.RedisInstance{
					{Name: "platform", Host: "rd-1"},
					{Name: "foghorn", Host: "rd-2"},
				},
			},
		},
	}
	targets := buildClusterDriftTargets(manifest)

	type identity struct{ host, display, deploy string }
	got := make(map[identity]struct{}, len(targets))
	for _, t := range targets {
		got[identity{t.Host, t.Display, t.Deploy}] = struct{}{}
	}
	want := []identity{
		{"pg-1", "yugabyte", "yugabyte"},
		{"pg-2", "yugabyte", "yugabyte"},
		{"ch-1", "clickhouse", "clickhouse"},
		{"kf-1", "kafka:broker", "kafka"},
		{"kc-1", "kafka:controller", "kafka-controller"},
		{"zk-1", "zookeeper", "zookeeper"},
		{"rd-1", "redis:platform", "redis-platform"},
		{"rd-2", "redis:foghorn", "redis-foghorn"},
	}
	if len(got) != len(want) {
		names := make([]string, 0, len(got))
		for id := range got {
			names = append(names, id.display+"@"+id.host)
		}
		sort.Strings(names)
		t.Fatalf("want %d targets, got %d: %v", len(want), len(got), names)
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("missing target %+v", w)
		}
	}
}

func TestCollectClusterDriftEntries_kafkaControllerProbedByControllerDeployName(t *testing.T) {
	t.Parallel()
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"kc-1": {Name: "kc-1"}},
		Infrastructure: inventory.InfrastructureConfig{
			Kafka: &inventory.KafkaConfig{
				Enabled: true, Mode: "native", Version: "3.7.0",
				Controllers: []inventory.KafkaController{{Host: "kc-1"}},
			},
		},
	}
	// Stub responds only to the correct deploy name. If drift probed the
	// wrong name, state would be nil and the entry would read `missing`.
	factory := func(host inventory.Host) clusterDetector {
		return &stubClusterDetector{responses: map[string]*detect.ServiceState{
			"kafka-controller": {Exists: true, Running: true, Mode: "native", Version: "3.7.0"},
		}}
	}
	entries := collectClusterDriftEntriesWith(context.Background(), manifest, nil, factory)
	if len(entries) != 1 || entries[0].Status != driftClusterOK || entries[0].Service != "kafka:controller" {
		t.Fatalf("want 1 ok entry labeled kafka:controller, got %+v", entries)
	}
}

func TestCollectClusterDriftEntries_redisInstanceProbedByInstanceDeployName(t *testing.T) {
	t.Parallel()
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"rd-1": {Name: "rd-1"}},
		Infrastructure: inventory.InfrastructureConfig{
			Redis: &inventory.RedisConfig{
				Enabled: true, Mode: "native", Version: "7.2",
				Instances: []inventory.RedisInstance{{Name: "platform", Host: "rd-1"}},
			},
		},
	}
	factory := func(host inventory.Host) clusterDetector {
		return &stubClusterDetector{responses: map[string]*detect.ServiceState{
			"redis-platform": {Exists: true, Running: true, Mode: "native", Version: "7.2"},
		}}
	}
	entries := collectClusterDriftEntriesWith(context.Background(), manifest, nil, factory)
	if len(entries) != 1 || entries[0].Status != driftClusterOK || entries[0].Service != "redis:platform" {
		t.Fatalf("want 1 ok entry labeled redis:platform, got %+v", entries)
	}
}

func TestCollectClusterDriftEntries_respectsManifestVersionPin(t *testing.T) {
	t.Parallel()
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"h1": {Name: "h1"}},
		Infrastructure: inventory.InfrastructureConfig{
			ClickHouse: &inventory.ClickHouseConfig{
				Enabled: true, Mode: "native", Version: "24.3", Host: "h1",
			},
		},
	}
	factory := func(host inventory.Host) clusterDetector {
		return &stubClusterDetector{responses: map[string]*detect.ServiceState{
			"clickhouse": {Exists: true, Running: true, Mode: "native", Version: "23.0"},
		}}
	}
	// gitopsManifest=nil: with no pin fallback, there'd be no wrong_version
	// check. With the pin at 24.3 and observed at 23.0, we must see drift.
	entries := collectClusterDriftEntriesWith(context.Background(), manifest, nil, factory)

	if len(entries) != 1 {
		t.Fatalf("want 1 entry, got %d: %+v", len(entries), entries)
	}
	if entries[0].Status != driftClusterWrongVersion {
		t.Errorf("want wrong_version, got %s (%+v)", entries[0].Status, entries[0])
	}
	if !strings.Contains(entries[0].Detail, "have 23.0, want 24.3") {
		t.Errorf("want detail 'have 23.0, want 24.3', got %q", entries[0].Detail)
	}
}

func TestRenderClusterDriftText_divergenceFooter(t *testing.T) {
	t.Parallel()
	rep := clusterDriftReport{
		Summary: clusterDriftSummary{Total: 5, Divergences: 2},
	}
	var buf bytes.Buffer
	renderClusterDriftText(&buf, rep)
	if !strings.Contains(buf.String(), "2 divergence(s) in 5 services") {
		t.Errorf("missing divergence footer; got:\n%s", buf.String())
	}
}
