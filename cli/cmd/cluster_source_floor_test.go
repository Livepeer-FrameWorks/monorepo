package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// floorsForNextRelease models the catalog the release after v0.3.11 declares: v0.3.12 requires a v0.3.11 source,
// because its version-blind readiness consumers probe /ready and v0.3.11 is the first release that serves it.
func floorsForNextRelease(version string) (string, bool) {
	switch version {
	case "v0.3.9", "v0.3.10", "v0.3.11":
		return "", true
	case "v0.3.12":
		return "v0.3.11", true
	default:
		return "", false
	}
}

func TestSourceFloorRefusal(t *testing.T) {
	cases := []struct {
		name      string
		fleet     []runningInstance
		target    string
		wantErr   []string
		wantClean bool
	}{
		{
			name:    "upgrade from a release without /ready is refused",
			fleet:   []runningInstance{{"bridge", "ctrl-1", "v0.3.10"}, {"commodore", "ctrl-1", "v0.3.10"}},
			target:  "v0.3.12",
			wantErr: []string{"bridge on ctrl-1 runs v0.3.10, below min_source_version v0.3.11 of v0.3.12; apply v0.3.11 first", "commodore on ctrl-1"},
		},
		{
			name:      "upgrade from the floor release proceeds",
			fleet:     []runningInstance{{"bridge", "ctrl-1", "v0.3.11"}, {"commodore", "ctrl-2", "v0.3.11-rc2"}},
			target:    "v0.3.12",
			wantClean: true,
		},
		{
			name:    "one replica left behind by an interrupted rolling upgrade is named",
			fleet:   []runningInstance{{"bridge", "ctrl-1", "v0.3.11"}, {"bridge", "ctrl-2", "v0.3.10"}, {"quartermaster", "ctrl-1", "v0.3.11"}},
			target:  "v0.3.12",
			wantErr: []string{"bridge on ctrl-2 runs v0.3.10"},
		},
		{
			name:      "a fleet mid-way through the floor-respecting upgrade proceeds",
			fleet:     []runningInstance{{"bridge", "ctrl-1", "v0.3.12"}, {"bridge", "ctrl-2", "v0.3.11"}},
			target:    "v0.3.12",
			wantClean: true,
		},
		{
			name:    "rollback below the running release's floor is refused",
			fleet:   []runningInstance{{"bridge", "ctrl-1", "v0.3.12"}},
			target:  "v0.3.10",
			wantErr: []string{"bridge on ctrl-1 runs v0.3.12, whose min_source_version is v0.3.11; moving back to v0.3.10 is not supported, roll back no further than v0.3.11"},
		},
		{
			name:      "rollback to the floor release proceeds",
			fleet:     []runningInstance{{"bridge", "ctrl-1", "v0.3.12"}, {"bridge", "ctrl-2", "v0.3.11"}},
			target:    "v0.3.11",
			wantClean: true,
		},
		{
			name:      "no floor in play: v0.3.11 upgrades from v0.3.10 and rolls back to it",
			fleet:     []runningInstance{{"bridge", "ctrl-1", "v0.3.10"}, {"bridge", "ctrl-2", "v0.3.11"}},
			target:    "v0.3.10",
			wantClean: true,
		},
		{
			name:      "a build past a tag counts as that tag",
			fleet:     []runningInstance{{"bridge", "ctrl-1", "v0.3.11-4-gabcdef1"}},
			target:    "v0.3.12",
			wantClean: true,
		},
		{
			name:    "an unreadable version is refused",
			fleet:   []runningInstance{{"bridge", "ctrl-1", ""}, {"steward", "ctrl-1", "dev"}},
			target:  "v0.3.12",
			wantErr: []string{`bridge on ctrl-1 reports ""`, `steward on ctrl-1 reports "dev"`},
		},
		{
			name:    "a running release newer than the catalog is refused",
			fleet:   []runningInstance{{"bridge", "ctrl-1", "v0.3.13"}},
			target:  "v0.3.12",
			wantErr: []string{"v0.3.13, which this CLI's release catalog does not declare"},
		},
		{
			name:    "a target the catalog does not declare is refused",
			fleet:   []runningInstance{{"bridge", "ctrl-1", "v0.3.12"}},
			target:  "v0.4.0",
			wantErr: []string{"target v0.4.0 is not declared"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := sourceFloorRefusal(tc.fleet, tc.target, floorsForNextRelease)
			if tc.wantClean {
				if err != nil {
					t.Fatalf("unexpected refusal: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected a refusal mentioning %q", tc.wantErr)
			}
			for _, want := range tc.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("refusal %q does not mention %q", err, want)
				}
			}
		})
	}
}

func TestEnforceSourceFloorSkipsDetectionWithoutDeclaredFloors(t *testing.T) {
	restore := stubSourceFloor(t, func() bool { return false }, floorsForNextRelease, func(context.Context, *ssh.Pool, *inventory.Manifest) ([]runningInstance, error) {
		t.Fatal("running versions were read although no release declares min_source_version")
		return nil, nil
	})
	defer restore()
	rc := &resolvedCluster{Manifest: &inventory.Manifest{}}
	if err := enforceSourceFloor(context.Background(), &bytes.Buffer{}, rc, nil, "v0.3.11"); err != nil {
		t.Fatalf("enforceSourceFloor: %v", err)
	}
}

func TestEnforceSourceFloorReadsFleetOncePerTarget(t *testing.T) {
	reads := 0
	fleet := []runningInstance{{"bridge", "ctrl-1", "v0.3.11"}}
	restore := stubSourceFloor(t, func() bool { return true }, floorsForNextRelease, func(context.Context, *ssh.Pool, *inventory.Manifest) ([]runningInstance, error) {
		reads++
		return fleet, nil
	})
	defer restore()
	rc := &resolvedCluster{Manifest: &inventory.Manifest{}}
	var out bytes.Buffer
	for range 3 {
		if err := enforceSourceFloor(context.Background(), &out, rc, nil, "v0.3.12"); err != nil {
			t.Fatalf("enforceSourceFloor: %v", err)
		}
	}
	if reads != 1 {
		t.Fatalf("fleet read %d times for one target, want 1", reads)
	}
	if !strings.Contains(out.String(), "min_source_version: v0.3.11") {
		t.Fatalf("output %q does not report the floor", out.String())
	}

	fleet = []runningInstance{{"bridge", "ctrl-1", "v0.3.12"}}
	if err := enforceSourceFloor(context.Background(), &out, rc, nil, "v0.3.10"); err == nil {
		t.Fatal("a rollback below the floor passed after an earlier target was verified")
	}
	if reads != 2 {
		t.Fatalf("a new target must re-read the fleet; reads = %d", reads)
	}
}

func TestEnforceSourceFloorFailsClosedOnDetectionError(t *testing.T) {
	restore := stubSourceFloor(t, func() bool { return true }, floorsForNextRelease, func(context.Context, *ssh.Pool, *inventory.Manifest) ([]runningInstance, error) {
		return nil, errors.New("ssh: connection refused")
	})
	defer restore()
	rc := &resolvedCluster{Manifest: &inventory.Manifest{}}
	err := enforceSourceFloor(context.Background(), &bytes.Buffer{}, rc, nil, "v0.3.12")
	if err == nil || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("err = %v, want the detection failure", err)
	}
	if rc.sourceFloorVerifiedFor != "" {
		t.Fatal("a failed read marked the target verified")
	}
}

type scriptedDetector struct {
	host   string
	states map[string]map[string]*detect.ServiceState
	calls  *[]string
}

func (d scriptedDetector) Detect(_ context.Context, name string) (*detect.ServiceState, error) {
	*d.calls = append(*d.calls, name+"@"+d.host)
	if st, ok := d.states[d.host][name]; ok {
		return st, nil
	}
	return &detect.ServiceState{ServiceName: name}, nil
}

// TestDetectRunningVersionsCoversEveryPlatformReplica drives the production fleet read over a scripted detector: every
// replica of every enabled platform service and interface is read under its deploy name, managed dependencies are
// skipped, and uninstalled replicas contribute nothing.
func TestDetectRunningVersionsCoversEveryPlatformReplica(t *testing.T) {
	manifest := &inventory.Manifest{
		Hosts: map[string]inventory.Host{
			"ctrl-1": {Name: "ctrl-1", ExternalIP: "10.0.0.1"},
			"ctrl-2": {Name: "ctrl-2", ExternalIP: "10.0.0.2"},
		},
		Services: map[string]inventory.ServiceConfig{
			"bridge":     {Enabled: true, Hosts: []string{"ctrl-1", "ctrl-2"}},
			"foghorn-eu": {Enabled: true, Host: "ctrl-2", Deploy: "foghorn"},
			"listmonk":   {Enabled: true, Host: "ctrl-1"},
			"purser":     {Enabled: false, Host: "ctrl-1"},
		},
		Interfaces: map[string]inventory.ServiceConfig{
			"chartroom": {Enabled: true, Host: "ctrl-1"},
		},
	}
	states := map[string]map[string]*detect.ServiceState{
		"ctrl-1": {
			"bridge":    {Exists: true, Version: "v0.3.11"},
			"chartroom": {Exists: true, Version: "v0.3.11"},
		},
		"ctrl-2": {
			"bridge":  {Exists: true, Version: "v0.3.10"},
			"foghorn": {Exists: true, Version: "v0.3.11"},
		},
	}
	var calls []string
	prev := newServiceDetectorFn
	newServiceDetectorFn = func(_ *ssh.Pool, host inventory.Host) serviceDetector {
		return scriptedDetector{host: host.Name, states: states, calls: &calls}
	}
	defer func() { newServiceDetectorFn = prev }()

	got, err := detectRunningVersions(context.Background(), nil, manifest)
	if err != nil {
		t.Fatalf("detectRunningVersions: %v", err)
	}
	want := map[string]string{"bridge@ctrl-1": "v0.3.11", "bridge@ctrl-2": "v0.3.10", "chartroom@ctrl-1": "v0.3.11", "foghorn@ctrl-2": "v0.3.11"}
	if len(got) != len(want) {
		t.Fatalf("instances = %+v, want %v", got, want)
	}
	for _, inst := range got {
		if want[inst.Service+"@"+inst.Host] != inst.Version {
			t.Errorf("unexpected instance %+v", inst)
		}
	}
	for _, call := range calls {
		if strings.HasPrefix(call, "listmonk@") || strings.HasPrefix(call, "purser@") {
			t.Errorf("detected %s; managed dependencies and disabled services are not part of the release floor", call)
		}
	}
	if err := sourceFloorRefusal(got, "v0.3.12", floorsForNextRelease); err == nil || !strings.Contains(err.Error(), "bridge on ctrl-2 runs v0.3.10") {
		t.Fatalf("mixed fleet refusal = %v, want bridge on ctrl-2 named", err)
	}
}

func TestReleaseMetadataEmitsEffectiveSourceFloor(t *testing.T) {
	run := func() string {
		cmd := newReleaseMetadataCmd()
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		cmd.SetArgs([]string{"v0.3.11"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("release-metadata: %v", err)
		}
		return buf.String()
	}
	if out := run(); strings.Contains(out, "min_source_version") {
		t.Fatalf("v0.3.11 declares no floor but metadata carries one:\n%s", out)
	}
	restore := stubSourceFloor(t, func() bool { return true }, func(v string) (string, bool) { return "v0.3.10", true }, detectRunningVersionsFn)
	defer restore()
	if out := run(); !strings.Contains(out, "min_source_version: v0.3.10\n") {
		t.Fatalf("metadata does not carry the effective floor:\n%s", out)
	}
}

func TestCheckFetchedSourceFloor(t *testing.T) {
	cases := []struct {
		name    string
		gm      gitops.Manifest
		wantErr string
	}{
		{name: "agreeing floor", gm: gitops.Manifest{PlatformVersion: "v0.3.12", MinSourceVersion: "v0.3.11"}},
		{name: "no floor on either side", gm: gitops.Manifest{PlatformVersion: "v0.3.11"}},
		{name: "fetched manifest omits the floor", gm: gitops.Manifest{PlatformVersion: "v0.3.12"}, wantErr: `fetched manifest ("") and this CLI's catalog ("v0.3.11")`},
		{name: "fetched manifest declares a different floor", gm: gitops.Manifest{PlatformVersion: "v0.3.11", MinSourceVersion: "v0.3.10"}, wantErr: "disagrees"},
		{name: "undeclared release with a floor", gm: gitops.Manifest{PlatformVersion: "v0.3.13", MinSourceVersion: "v0.3.12"}, wantErr: "not in this CLI's release catalog"},
		{name: "undeclared release without a floor", gm: gitops.Manifest{PlatformVersion: "v0.3.13"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkFetchedSourceFloor(&tc.gm, floorsForNextRelease)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func stubSourceFloor(t *testing.T, declared func() bool, floorFor func(string) (string, bool), read func(context.Context, *ssh.Pool, *inventory.Manifest) ([]runningInstance, error)) func() {
	t.Helper()
	prevDeclared, prevFloor, prevRead := sourceFloorsDeclaredFn, sourceFloorForFn, detectRunningVersionsFn
	sourceFloorsDeclaredFn, sourceFloorForFn, detectRunningVersionsFn = declared, floorFor, read
	return func() {
		sourceFloorsDeclaredFn, sourceFloorForFn, detectRunningVersionsFn = prevDeclared, prevFloor, prevRead
	}
}
