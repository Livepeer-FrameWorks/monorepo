package cmd

import (
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"frameworks/cli/pkg/inventory"
)

// twoCellFoghornManifest runs one Foghorn service on two replicas in cell-a
// and one in cell-b.
func twoCellFoghornManifest() *inventory.Manifest {
	return &inventory.Manifest{
		Profile: "dev",
		Hosts: map[string]inventory.Host{
			"a-1": {Name: "a-1", Cluster: "cell-a"},
			"a-2": {Name: "a-2", Cluster: "cell-a"},
			"b-1": {Name: "b-1", Cluster: "cell-b"},
		},
		Clusters: map[string]inventory.ClusterConfig{"cell-a": {}, "cell-b": {}},
		Services: map[string]inventory.ServiceConfig{
			"foghorn": {Enabled: true, Hosts: []string{"a-1", "a-2", "b-1"}},
		},
	}
}

// cellReads scripts the gate's reads of each cell and records them in the
// rollout's event log.
type cellReads struct {
	mu     sync.Mutex
	events *[]string
	states map[string][]mediaCellAuthority
}

func (r *cellReads) read(_ context.Context, cell string) (mediaCellAuthority, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	*r.events = append(*r.events, "read:"+cell)
	states := r.states[cell]
	if len(states) == 0 {
		return mediaCellAuthority{}, errors.New("no scripted state")
	}
	state := states[0]
	if len(states) > 1 {
		r.states[cell] = states[1:]
	}
	return state, nil
}

func runGatedFoghornUpgrade(t *testing.T, states map[string][]mediaCellAuthority, settle time.Duration) ([]string, error) {
	t.Helper()
	var events []string
	var mu sync.Mutex
	reads := &cellReads{events: &events, states: states}
	gate := &mediaCellGate{out: io.Discard, timeout: 100 * time.Millisecond, interval: time.Millisecond, settle: settle,
		read: func(ctx context.Context, cell string) (mediaCellAuthority, error) {
			mu.Lock()
			defer mu.Unlock()
			return reads.read(ctx, cell)
		}}
	manifest := twoCellFoghornManifest()
	waves, after := upgradeRolloutWaves(context.Background(), manifest, "foghorn", "foghorn", []string{"a-1", "a-2", "b-1"}, gate)
	err := runRolloutWaves(context.Background(), io.Discard, waves, func(_ context.Context, host string, _ io.Writer) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, "upgrade:"+host)
		return nil
	}, after)
	return events, err
}

// The next cell starts only once the cell just upgraded holds no schema-1
// tenant: cell-a's two stranded tenants hold cell-b until Commodore publishes
// their schema-2 authority.
func TestFoghornUpgradeHoldsTheNextCellUntilTheUpgradedCellConverges(t *testing.T) {
	ready := mediaCellAuthority{Tracked: true, Ready: true}
	stranded := mediaCellAuthority{Tracked: true, Ready: true, LegacyTenants: 2}
	events, err := runGatedFoghornUpgrade(t, map[string][]mediaCellAuthority{
		"cell-a": {stranded, stranded, ready},
		"cell-b": {ready},
	}, time.Hour)
	if err != nil {
		t.Fatalf("rollout: %v", err)
	}
	want := []string{"upgrade:a-1", "upgrade:a-2", "read:cell-a", "read:cell-a", "read:cell-a", "upgrade:b-1", "read:cell-b"}
	if !slices.Equal(events, want) {
		t.Fatalf("events = %v\nwant     %v", events, want)
	}
}

// A cell that keeps a schema-1 tenant stops the rollout before the next cell,
// naming the cell, the count, and where to look.
func TestFoghornUpgradeStopsBeforeTheNextCellWhenTenantsStayOnSchemaOne(t *testing.T) {
	stranded := mediaCellAuthority{Tracked: true, Ready: true, LegacyTenants: 3}
	events, err := runGatedFoghornUpgrade(t, map[string][]mediaCellAuthority{"cell-a": {stranded}}, time.Hour)
	var stopped *rolloutStoppedError
	if !errors.As(err, &stopped) || !slices.Equal(stopped.NotStarted, []string{"b-1"}) {
		t.Fatalf("err = %v; want the rollout stopped with b-1 untouched", err)
	}
	for _, want := range []string{"cell cell-a: 3 tenant authority(ies) still on schema 1", "later cells were not upgraded", "frameworks cluster diagnose media-authority"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("err = %v; want %q", err, want)
		}
	}
	if slices.Contains(events, "upgrade:b-1") {
		t.Fatalf("cell-b upgraded while cell-a held schema-1 tenants: %v", events)
	}
}

// A cell not yet attesting enforcement is given the settle time for its
// attestation to reach Commodore, then passes when it holds no schema-1 tenant.
func TestFoghornUpgradeWaitsForANewAttestationBeforeTheNextCell(t *testing.T) {
	pending := mediaCellAuthority{Tracked: true}
	events, err := runGatedFoghornUpgrade(t, map[string][]mediaCellAuthority{
		"cell-a": {pending},
		"cell-b": {pending},
	}, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("rollout: %v", err)
	}
	reads := 0
	for _, event := range events {
		if event == "read:cell-a" {
			reads++
		}
		if event == "upgrade:b-1" {
			break
		}
	}
	if reads < 2 {
		t.Fatalf("cell-a was read %d time(s) before cell-b; an unattested cell must be read again after the settle time: %v", reads, events)
	}
}

func TestParseMediaCellAuthority(t *testing.T) {
	values := parseMediaAuthorityDoctorOutput("######## database commodore\ncapabilities_table=1\ncell_ready:cell-a=1\ncell_ready:cell-b=0\nlegacy_schema_tenants_on_ready_cells=4\nlegacy_schema_tenants_cell:cell-a=4\n")["commodore"]
	if got, err := parseMediaCellAuthority(values, "cell-a"); err != nil || got != (mediaCellAuthority{Tracked: true, Ready: true, LegacyTenants: 4}) {
		t.Fatalf("cell-a = %+v, %v", got, err)
	}
	if got, err := parseMediaCellAuthority(values, "cell-b"); err != nil || got != (mediaCellAuthority{Tracked: true}) {
		t.Fatalf("cell-b = %+v, %v", got, err)
	}
	if got, err := parseMediaCellAuthority(map[string]int64{"capabilities_table": 0}, "cell-a"); err != nil || got.Tracked {
		t.Fatalf("untracked = %+v, %v", got, err)
	}
	if _, err := parseMediaCellAuthority(map[string]int64{"capabilities_table": 1}, "cell-a"); err == nil {
		t.Fatal("a probe that stopped before the tenant count read as zero")
	}
}
