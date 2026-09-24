package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"

	infra "github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	"github.com/spf13/cobra"
)

func TestRolloutHostTiersTakeTheMostCriticalPlacement(t *testing.T) {
	maxThree := 3
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"core":      {Name: "core"},
			"yb-1":      {Name: "yb-1"},
			"broker":    {Name: "broker"},
			"media-a":   {Name: "media-a", Cluster: "cell-a"},
			"redis-a":   {Name: "redis-a", Cluster: "cell-a"},
			"analytics": {Name: "analytics"},
			"mixed":     {Name: "mixed"},
			"edge-1":    {Name: "edge-1", Roles: []string{infra.NodeTypeEdge}},
			"mesh-only": {Name: "mesh-only"},
			"novel":     {Name: "novel"},
		},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster": {Enabled: true, Host: "core"},
			"foghorn-a":     {Enabled: true, Deploy: "foghorn", Hosts: []string{"media-a", "mixed"}},
			"decklog":       {Enabled: true, Hosts: []string{"analytics", "mixed"}, UpdateStrategy: &inventory.UpdateStrategyConfig{MaxUnavailable: &maxThree}},
			"bridge":        {Enabled: true, Host: "mixed"},
			"privateer":     {Enabled: true, Hosts: []string{"core", "mesh-only", "analytics"}},
			"brand-new":     {Enabled: true, Host: "novel"},
			"disabled":      {Enabled: false, Host: "mesh-only", Deploy: "quartermaster"},
		},
		Interfaces: map[string]inventory.ServiceConfig{
			"chartroom": {Enabled: true, Host: "analytics"},
		},
		Infrastructure: inventory.InfrastructureConfig{
			Postgres: &inventory.PostgresConfig{Enabled: true, Engine: "yugabyte", Nodes: []inventory.PostgresNode{{Host: "yb-1", ID: 1}}},
			Kafka:    &inventory.KafkaConfig{Enabled: true, ClusterID: "k", Brokers: []inventory.KafkaBroker{{Host: "broker", ID: 1}}},
			Redis:    &inventory.RedisConfig{Enabled: true, Instances: []inventory.RedisInstance{{Name: "foghorn", Host: "redis-a"}}},
		},
	}
	want := map[string]rolloutTier{
		"core":      rolloutTierControl,
		"yb-1":      rolloutTierControl,
		"broker":    rolloutTierControl,
		"media-a":   rolloutTierMedia,
		"redis-a":   rolloutTierMedia,
		"analytics": rolloutTierOther,
		"mixed":     rolloutTierControl, // bridge outranks foghorn and decklog
		"edge-1":    rolloutTierMedia,
		"novel":     rolloutTierControl, // unclassified services roll one host at a time
	}
	if got := rolloutHostTiers(manifest); !reflect.DeepEqual(got, want) {
		t.Fatalf("host tiers = %v, want %v", got, want)
	}
}

func TestBuildHostConvergenceWavesOrdersCanaryControlMediaOther(t *testing.T) {
	tiers := map[string]rolloutTier{
		"c1": rolloutTierControl, "c2": rolloutTierControl, "c3": rolloutTierControl,
		"ma1": rolloutTierMedia, "ma2": rolloutTierMedia, "mb1": rolloutTierMedia,
	}
	cells := map[string]string{"ma1": "cell-a", "ma2": "cell-a", "mb1": "cell-b"}
	var hosts []string
	for i := range 10 {
		hosts = append(hosts, fmt.Sprintf("o%02d", i))
	}
	hosts = append(hosts, "mb1", "c1", "ma1", "c2", "ma2", "c3")
	waves := buildHostConvergenceWaves(hosts, func(h string) rolloutTier { return tiers[h] }, func(h string) string { return cells[h] })

	want := []rolloutWave{
		{Name: "canary", Lanes: [][]string{{"c1"}}, Limit: 1},
		{Name: "control", Lanes: [][]string{{"c2", "c3"}}, Limit: 1},
		{Name: "media", Lanes: [][]string{{"ma1", "ma2"}, {"mb1"}}, Limit: 2},
		{Name: "other", Lanes: singleHostLanes(hosts[:10]), Limit: rolloutOtherHostConcurrency},
	}
	if !reflect.DeepEqual(waves, want) {
		t.Fatalf("waves =\n%+v\nwant\n%+v", waves, want)
	}
	if got := waves[3].concurrency(); got != 8 {
		t.Fatalf("other wave concurrency = %d, want 8", got)
	}
}

func TestBuildHostConvergenceWavesCanaryWithoutControlHosts(t *testing.T) {
	tiers := map[string]rolloutTier{"m1": rolloutTierMedia, "m2": rolloutTierMedia}
	waves := buildHostConvergenceWaves([]string{"o1", "m1", "m2"}, func(h string) rolloutTier { return tiers[h] }, func(h string) string { return h })
	var names []string
	for _, wave := range waves {
		names = append(names, wave.describe())
	}
	want := []string{"canary: m1", "media: m2", "other: o1"}
	if !slices.Equal(names, want) {
		t.Fatalf("waves = %v, want %v", names, want)
	}
}

func TestBuildReplicaWavesByTierAndMaxUnavailable(t *testing.T) {
	cellOf := func(h string) string { return strings.SplitN(h, "-", 2)[0] }
	hosts := []string{"eu-1", "us-1", "eu-2", "us-2"}
	cases := []struct {
		name  string
		tier  rolloutTier
		maxU  int
		want  []rolloutWave
		conc  []int
		descr string
	}{
		{
			name: "control canary then serial",
			tier: rolloutTierControl, maxU: 4,
			want: []rolloutWave{
				{Name: "canary", Lanes: [][]string{{"eu-1"}}, Limit: 1},
				{Name: "control", Lanes: [][]string{{"us-1", "eu-2", "us-2"}}, Limit: 1},
			},
			conc: []int{1, 1},
		},
		{
			name: "media one per cell, cells in parallel",
			tier: rolloutTierMedia, maxU: 2,
			want: []rolloutWave{{Name: "media", Lanes: [][]string{{"eu-1", "eu-2"}, {"us-1", "us-2"}}, Limit: 2}},
			conc: []int{2},
		},
		{
			name: "media capped by max_unavailable",
			tier: rolloutTierMedia, maxU: 1,
			want: []rolloutWave{{Name: "media", Lanes: [][]string{{"eu-1", "eu-2"}, {"us-1", "us-2"}}, Limit: 1}},
			conc: []int{1},
		},
		{
			name: "other capped by max_unavailable",
			tier: rolloutTierOther, maxU: 3,
			want: []rolloutWave{{Name: "other", Lanes: singleHostLanes(hosts), Limit: 3}},
			conc: []int{3},
		},
	}
	for _, tc := range cases {
		got := buildReplicaWaves(tc.tier, hosts, cellOf, tc.maxU)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: waves = %+v, want %+v", tc.name, got, tc.want)
			continue
		}
		for i, wave := range got {
			if wave.concurrency() != tc.conc[i] {
				t.Errorf("%s: wave %d concurrency = %d, want %d", tc.name, i, wave.concurrency(), tc.conc[i])
			}
		}
	}
}

func TestUpgradeReplicaWavesUseTheEffectiveUpdateStrategy(t *testing.T) {
	three := 3
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{"a": {Name: "a"}, "b": {Name: "b"}, "c": {Name: "c"}},
		Services: map[string]inventory.ServiceConfig{
			"decklog":   {Enabled: true, Hosts: []string{"a", "b", "c"}},
			"signalman": {Enabled: true, Hosts: []string{"a", "b", "c"}, UpdateStrategy: &inventory.UpdateStrategyConfig{MaxUnavailable: &three}},
		},
	}
	hosts := []string{"a", "b", "c"}
	if waves := upgradeReplicaWaves(manifest, "decklog", "decklog", hosts); len(waves) != 1 || waves[0].concurrency() != 3 {
		t.Fatalf("decklog waves = %+v, want every replica at once under the OTHER-tier default", waves)
	}
	if waves := upgradeReplicaWaves(manifest, "nginx", "nginx", hosts); len(waves) != 1 || waves[0].concurrency() != 1 {
		t.Fatalf("nginx waves = %+v, want the ingress front door serial", waves)
	}
	if waves := upgradeReplicaWaves(manifest, "signalman", "signalman", hosts); len(waves) != 1 || waves[0].concurrency() != 3 {
		t.Fatalf("signalman waves = %+v, want every replica at once under max_unavailable=3", waves)
	}
	if waves := upgradeReplicaWaves(manifest, "quartermaster", "quartermaster", []string{"a"}); len(waves) != 1 || waves[0].Name != "control" {
		t.Fatalf("single-replica waves = %+v, want one wave named by tier", waves)
	}
}

// recordingHosts records which hosts ran and fails the named ones.
type recordingHosts struct {
	mu   sync.Mutex
	ran  []string
	fail map[string]bool
}

func (r *recordingHosts) run(_ context.Context, host string, out io.Writer) error {
	r.mu.Lock()
	r.ran = append(r.ran, host)
	r.mu.Unlock()
	fmt.Fprintf(out, "converging\nstill converging")
	if r.fail[host] {
		return errors.New("boom")
	}
	return nil
}

func TestRunRolloutWavesStopsLaterWavesOnFailure(t *testing.T) {
	waves := []rolloutWave{
		{Name: "canary", Lanes: [][]string{{"c1"}}, Limit: 1},
		{Name: "control", Lanes: [][]string{{"c2", "c3"}}, Limit: 1},
		{Name: "other", Lanes: singleHostLanes([]string{"o1", "o2"}), Limit: 8},
	}
	rec := &recordingHosts{fail: map[string]bool{"c2": true}}
	var out bytes.Buffer
	err := runRolloutWaves(context.Background(), &out, waves, rec.run, nil)
	var stopped *rolloutStoppedError
	if !errors.As(err, &stopped) {
		t.Fatalf("err = %v, want *rolloutStoppedError", err)
	}
	if !slices.Equal(rec.ran, []string{"c1", "c2"}) {
		t.Fatalf("ran = %v, want the canary and the failing host only", rec.ran)
	}
	if stopped.Wave != "control" || !slices.Equal(stopped.NotStarted, []string{"c3", "o1", "o2"}) {
		t.Fatalf("stopped = %+v, want control wave with c3, o1, o2 untouched", stopped)
	}
	if !strings.Contains(err.Error(), "not started (untouched): c3, o1, o2") {
		t.Fatalf("error does not name the untouched hosts: %v", err)
	}
	if strings.Contains(out.String(), "[wave 3/3]") {
		t.Fatalf("a later wave started after the failure:\n%s", out.String())
	}
}

func TestRunRolloutWavesGateFailureStopsLaterWaves(t *testing.T) {
	waves := []rolloutWave{
		{Name: "canary", Lanes: [][]string{{"c1"}}, Limit: 1},
		{Name: "control", Lanes: [][]string{{"c2"}}, Limit: 1},
	}
	rec := &recordingHosts{}
	err := runRolloutWaves(context.Background(), io.Discard, waves, rec.run, func(wave rolloutWave) error {
		if wave.Name == "canary" {
			return errors.New("mesh unhealthy")
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "mesh unhealthy") {
		t.Fatalf("err = %v, want the canary gate failure", err)
	}
	if !slices.Equal(rec.ran, []string{"c1"}) {
		t.Fatalf("ran = %v, want only the canary", rec.ran)
	}
}

func TestRunRolloutWavesRunsLanesConcurrentlyWithPrefixedLines(t *testing.T) {
	waves := []rolloutWave{{Name: "media", Lanes: [][]string{{"a1"}, {"b1"}}, Limit: 2}}
	// Each host waits for the other to start, so a serial wave times out.
	var started sync.WaitGroup
	started.Add(2)
	run := func(_ context.Context, host string, out io.Writer) error {
		started.Done()
		done := make(chan struct{})
		go func() { started.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			return errors.New("lanes did not run concurrently")
		}
		fmt.Fprintf(out, "line one\nline two")
		return nil
	}
	var out bytes.Buffer
	if err := runRolloutWaves(context.Background(), &out, waves, run, nil); err != nil {
		t.Fatalf("runRolloutWaves: %v", err)
	}
	for _, want := range []string{"[a1] line one\n", "[a1] line two\n", "[b1] line one\n", "[b1] line two\n"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunRolloutWavesParallelLaneStopsAfterSiblingFailure(t *testing.T) {
	waves := []rolloutWave{{Name: "media", Lanes: [][]string{{"a1", "a2"}, {"b1", "b2"}}, Limit: 2}}
	a1Failed := make(chan struct{})
	b1Started := make(chan struct{})
	var mu sync.Mutex
	var ran []string
	run := func(_ context.Context, host string, _ io.Writer) error {
		mu.Lock()
		ran = append(ran, host)
		mu.Unlock()
		switch host {
		case "a1":
			// Fail only once the sibling lane is running, so both lanes are
			// provably in flight when the failure lands.
			<-b1Started
			defer close(a1Failed)
			return errors.New("boom")
		case "b1":
			close(b1Started)
			<-a1Failed
		}
		return nil
	}
	err := runRolloutWaves(context.Background(), io.Discard, waves, run, nil)
	var stopped *rolloutStoppedError
	if !errors.As(err, &stopped) {
		t.Fatalf("err = %v, want *rolloutStoppedError", err)
	}
	slices.Sort(ran)
	if !slices.Equal(ran, []string{"a1", "b1"}) {
		t.Fatalf("ran = %v, want no host started after a1 failed", ran)
	}
	notStarted := slices.Clone(stopped.NotStarted)
	slices.Sort(notStarted)
	if !slices.Equal(notStarted, []string{"a2", "b2"}) {
		t.Fatalf("not started = %v, want a2 and b2", notStarted)
	}
}

// meshProvisioner is a Privateer provisioner whose role always reports drift
// and records each converged host; it fails the hosts in fail.
type meshProvisioner struct {
	fakeTaskProvisioner
	mu     *sync.Mutex
	events *[]string
	fail   map[string]bool
}

func (m *meshProvisioner) Detect(context.Context, inventory.Host) (*detect.ServiceState, error) {
	return &detect.ServiceState{Exists: true, Running: true}, nil
}

func (m *meshProvisioner) WouldChange(context.Context, inventory.Host, provisioner.ServiceConfig, []string) (bool, error) {
	return true, nil
}

func (m *meshProvisioner) Provision(_ context.Context, host inventory.Host, _ provisioner.ServiceConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	*m.events = append(*m.events, "provision:"+host.Name)
	if m.fail[host.Name] {
		return errors.New("role failed")
	}
	return nil
}

func runPrivateerConvergence(t *testing.T, failHost, unhealthyHost string) ([]string, error) {
	t.Helper()
	manifest := multiRegionReleaseManifest()
	for name, host := range manifest.Hosts {
		host.WireguardPrivateKey = "private-" + name
		manifest.Hosts[name] = host
	}
	plan, err := orchestrator.NewPlanner(manifest).Plan(context.Background(), orchestrator.ProvisionOptions{Phase: orchestrator.PhaseAll})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var steps []releaseHostConvergenceStep
	for _, step := range planReleaseHostConvergence(plan, manifest) {
		if step.Kind == releaseHostStepPrivateer {
			steps = append(steps, step)
		}
	}

	var mu sync.Mutex
	var events []string
	fail := map[string]bool{failHost: true}
	original := taskProvisioner
	// Production builds a provisioner per task, and hosts in a batch converge
	// concurrently, so each task gets its own fake sharing only the event log.
	taskProvisioner = func(string, *ssh.Pool) (provisioner.Provisioner, error) {
		return &meshProvisioner{mu: &mu, events: &events, fail: fail}, nil
	}
	t.Cleanup(func() { taskProvisioner = original })

	cmd := &cobra.Command{}
	cmd.SetOut(io.Discard)
	c := &releaseHostConvergence{
		cmd:         cmd,
		manifest:    manifest,
		runtimeData: map[string]any{"service_token": "service-token"},
		sharedEnv:   map[string]string{"SERVICE_TOKEN": "service-token"},
		verifyMeshFn: func(_ context.Context, hosts []string) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, "mesh:"+strings.Join(hosts, ","))
			if slices.Contains(hosts, unhealthyHost) {
				return errors.New("quartermaster.internal does not resolve")
			}
			return nil
		},
	}
	err = c.run(context.Background(), steps, false)
	return events, err
}

func TestReleasePrivateerConvergenceRunsWavesGatedOnMeshHealth(t *testing.T) {
	events, err := runPrivateerConvergence(t, "", "")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Canary first and gated alone; the remaining hosts converge as one batch
	// in parallel, then the batch is gated on mesh health.
	if len(events) != 5 || events[0] != "provision:regional-eu-1" || events[1] != "mesh:regional-eu-1" {
		t.Fatalf("events = %v, want the canary provisioned and gated first", events)
	}
	batch := slices.Clone(events[2:4])
	slices.Sort(batch)
	if !slices.Equal(batch, []string{"provision:central-eu-1", "provision:regional-us-1"}) || !strings.HasPrefix(events[4], "mesh:") {
		t.Fatalf("events = %v, want both remaining hosts provisioned before the batch mesh gate", events)
	}
}

func TestReleasePrivateerConvergenceFailedCanaryLeavesOtherHostsUntouched(t *testing.T) {
	events, err := runPrivateerConvergence(t, "regional-eu-1", "")
	if err == nil || !strings.Contains(err.Error(), "not started (untouched): regional-us-1, central-eu-1") {
		t.Fatalf("err = %v, want the canary failure naming the untouched hosts", err)
	}
	if !slices.Equal(events, []string{"provision:regional-eu-1"}) {
		t.Fatalf("events = %v, want only the canary provisioned and no mesh gate", events)
	}
}

func TestReleasePrivateerConvergenceUnhealthyCanaryMeshStopsRollout(t *testing.T) {
	events, err := runPrivateerConvergence(t, "", "regional-eu-1")
	if err == nil || !strings.Contains(err.Error(), "mesh health after Privateer convergence") {
		t.Fatalf("err = %v, want the canary mesh gate failure", err)
	}
	if !slices.Equal(events, []string{"provision:regional-eu-1", "mesh:regional-eu-1"}) {
		t.Fatalf("events = %v, want nothing converged after the unhealthy canary", events)
	}
}

func TestReleaseHostConvergenceResumeHint(t *testing.T) {
	var out bytes.Buffer
	writeReleaseHostConvergenceResumeHint(&out, "v0.3.12", false)
	if !strings.Contains(out.String(), "rerun `frameworks cluster release apply --version v0.3.12`") {
		t.Fatalf("hint = %q", out.String())
	}
	out.Reset()
	writeReleaseHostConvergenceResumeHint(&out, "v0.3.12", true)
	if out.Len() != 0 {
		t.Fatalf("dry-run hint = %q, want none", out.String())
	}
}
