package cmd

import (
	"context"
	"errors"
	"strings"
	"testing"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
)

// priorPreflightManifest places every service with a required data migration up to v0.3.11 on ctrl-1, without any
// database engine, so enforcePriorReleasesComplete reads only data-migration state.
func priorPreflightManifest() *inventory.Manifest {
	return &inventory.Manifest{
		Hosts: map[string]inventory.Host{"ctrl-1": {Name: "ctrl-1", ExternalIP: "192.0.2.10", User: "root"}},
		Services: map[string]inventory.ServiceConfig{
			"quartermaster": {Enabled: true, Host: "ctrl-1"},
			"commodore":     {Enabled: true, Host: "ctrl-1"},
		},
	}
}

func stubPriorDataMigrationSource(t *testing.T, src datamigrate.StateSource) {
	t.Helper()
	prev := priorDataMigrationSourceFn
	t.Cleanup(func() { priorDataMigrationSourceFn = prev })
	priorDataMigrationSourceFn = func(*ssh.Pool, *inventory.Manifest) datamigrate.StateSource { return src }
}

// TestEnforcePriorReleasesCompleteReportsUnreadableState pins the refusal for a data-migration state the CLI could not
// read: it fails closed and names the error and host, but does not claim the release is unfinished or tell the
// operator to apply an earlier release.
func TestEnforcePriorReleasesCompleteReportsUnreadableState(t *testing.T) {
	stubPriorDataMigrationSource(t, func(_ context.Context, service, id string) datamigrate.LiveStatus {
		if service == "quartermaster" {
			return datamigrate.LiveStatus{ID: id, Service: service, FetchError: errors.New("ssh run: Process exited with status 255")}
		}
		return datamigrate.LiveStatus{ID: id, Service: service, Status: datamigrate.StatusCompleted}
	})
	var out strings.Builder
	err := enforcePriorReleasesComplete(context.Background(), &out, &resolvedCluster{Manifest: priorPreflightManifest()}, nil, "v0.3.11")
	if err == nil {
		t.Fatal("an unreadable required data migration must still refuse")
	}
	msg := err.Error()
	for _, want := range []string{"could not be read", "quartermaster/quartermaster_node_identity_keys_v0_3_0", "on ctrl-1", "Process exited with status 255"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal does not mention %q:\n%s", want, msg)
		}
	}
	for _, unwanted := range []string{"has not completed release", "release apply --version", "data-migrate run"} {
		if strings.Contains(msg, unwanted) {
			t.Errorf("refusal for an unreadable state says %q:\n%s", unwanted, msg)
		}
	}
}

// TestEnforcePriorReleasesCompleteReportsInterrupt pins that an interrupt during the state reads (the operator's
// Ctrl+C kills the ssh child, which exits 255, and cancels the command context) is reported as an interrupt.
func TestEnforcePriorReleasesCompleteReportsInterrupt(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stubPriorDataMigrationSource(t, func(_ context.Context, service, id string) datamigrate.LiveStatus {
		cancel()
		return datamigrate.LiveStatus{ID: id, Service: service, FetchError: errors.New("ssh run: Process exited with status 255")}
	})
	var out strings.Builder
	err := enforcePriorReleasesComplete(ctx, &out, &resolvedCluster{Manifest: priorPreflightManifest()}, nil, "v0.3.11")
	if err == nil || !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("err = %v; want an interrupt carrying context.Canceled", err)
	}
	if strings.Contains(err.Error(), "has not completed release") || strings.Contains(err.Error(), "release apply --version") {
		t.Fatalf("an interrupt was reported as an unfinished release:\n%v", err)
	}
}

// TestEnforcePriorReleasesCompleteAnnouncesEachStateRead pins a progress line before every data-migration state read,
// each of which is several sequential SSH calls.
func TestEnforcePriorReleasesCompleteAnnouncesEachStateRead(t *testing.T) {
	stubPriorDataMigrationSource(t, func(_ context.Context, service, id string) datamigrate.LiveStatus {
		return datamigrate.LiveStatus{ID: id, Service: service, Status: datamigrate.StatusCompleted}
	})
	var out strings.Builder
	if err := enforcePriorReleasesComplete(context.Background(), &out, &resolvedCluster{Manifest: priorPreflightManifest()}, nil, "v0.3.11"); err != nil {
		t.Fatalf("enforcePriorReleasesComplete: %v", err)
	}
	for _, want := range []string{
		"[preflight] reading data-migration state: quartermaster/quartermaster_node_identity_keys_v0_3_0 on ctrl-1\n",
		"[preflight] reading data-migration state: commodore/commodore_pull_source_pins_to_stream_rules_v0_3_8 on ctrl-1\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output does not contain %q:\n%s", want, out.String())
		}
	}
}

// TestReleaseApplyAnnouncesPreflightPhases pins progress output for every read-only preflight phase of release apply,
// so the SSH reads between the plan summary and the first mutation are never silent.
func TestReleaseApplyAnnouncesPreflightPhases(t *testing.T) {
	h := &releaseApplyHarness{}
	h.install(t)
	out, _ := h.apply(t, true)
	for _, want := range []string{
		"[preflight] resolving release artifacts for ",
		"[preflight] checking that every release before v0.3.11 is complete\n",
		"[preflight] reading service database state on db-1 (1 database(s))\n",
		"[preflight] reading postgres postdeploy ledger through v0.3.10 on db-1\n",
		"[preflight] reading data-migration state: commodore/commodore_pull_source_pins_to_stream_rules_v0_3_8 on ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("release apply output does not contain %q:\n%s", want, out)
		}
	}
}

// TestEnforceSourceFloorAnnouncesEachDetection pins a progress line before every replica the source floor detects.
func TestEnforceSourceFloorAnnouncesEachDetection(t *testing.T) {
	prevDeclared, prevFloor, prevDetector := sourceFloorsDeclaredFn, sourceFloorForFn, newServiceDetectorFn
	t.Cleanup(func() {
		sourceFloorsDeclaredFn, sourceFloorForFn, newServiceDetectorFn = prevDeclared, prevFloor, prevDetector
	})
	sourceFloorsDeclaredFn = func() bool { return true }
	sourceFloorForFn = floorsForNextRelease
	var calls []string
	states := map[string]map[string]*detect.ServiceState{"ctrl-1": {"bridge": {Exists: true, Version: "v0.3.11"}}}
	newServiceDetectorFn = func(_ *ssh.Pool, host inventory.Host) serviceDetector {
		return scriptedDetector{host: host.Name, states: states, calls: &calls}
	}
	manifest := &inventory.Manifest{
		Hosts:    map[string]inventory.Host{"ctrl-1": {Name: "ctrl-1", ExternalIP: "192.0.2.10"}},
		Services: map[string]inventory.ServiceConfig{"bridge": {Enabled: true, Host: "ctrl-1"}},
	}
	var out strings.Builder
	if err := enforceSourceFloor(context.Background(), &out, &resolvedCluster{Manifest: manifest}, nil, "v0.3.12"); err != nil {
		t.Fatalf("enforceSourceFloor: %v", err)
	}
	if !strings.Contains(out.String(), "[source floor] detecting bridge on ctrl-1\n") {
		t.Fatalf("output does not announce the bridge detection on ctrl-1:\n%s", out.String())
	}
}
