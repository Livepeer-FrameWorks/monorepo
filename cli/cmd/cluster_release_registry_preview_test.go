package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/cli/pkg/bootstrap"
	"frameworks/cli/pkg/ssh"
	"github.com/spf13/cobra"
)

func TestRegistryRetirementPreviewProtectsUnownedAndRuntimeRows(t *testing.T) {
	scope := &bootstrap.ServiceRegistryScope{Owner: "production", Clusters: []string{"core"}, Complete: true,
		Adopt: []bootstrap.ServiceRegistryAdoption{{InstanceID: "legacy-reviewed", ServiceName: "retired", ClusterID: "core", NodeID: "old", Protocol: "tcp", Port: 9000}},
	}
	desired := bootstrap.QuartermasterSection{RegistryScope: scope, ServiceRegistry: []bootstrap.ServiceRegistryEntry{{ServiceName: "kept", ClusterID: "core", NodeID: "node", Protocol: "http", Port: 8080}}}
	rows := []registryPreviewRow{
		{InstanceID: "managed-kept", ServiceName: "kept", ClusterID: "core", NodeID: "node", Protocol: "http", Port: 8080, Source: "manifest", Owner: "production", Status: "running"},
		{InstanceID: "managed-removed", ServiceName: "old", ClusterID: "core", NodeID: "old", Protocol: "http", Port: 8081, Source: "manifest", Owner: "production", Status: "running"},
		{InstanceID: "legacy-reviewed", ServiceName: "retired", ClusterID: "core", NodeID: "old", Protocol: "tcp", Port: 9000, Source: "legacy", Status: "running"},
		{InstanceID: "runtime", ServiceName: "old", ClusterID: "core", NodeID: "other", Protocol: "http", Port: 8080, Source: "runtime", Status: "running"},
		{InstanceID: "other-owner", ServiceName: "old", ClusterID: "core", NodeID: "other", Protocol: "http", Port: 8080, Source: "manifest", Owner: "staging", Status: "running"},
		{InstanceID: "unreviewed", ServiceName: "old", ClusterID: "core", NodeID: "other", Protocol: "http", Port: 8080, Source: "legacy", Status: "running"},
		{InstanceID: "outside", ServiceName: "old", ClusterID: "other", NodeID: "other", Protocol: "http", Port: 8080, Source: "manifest", Owner: "production", Status: "running"},
	}
	stopped, err := registryRetirementPreview(desired, rows)
	if err != nil || len(stopped) != 2 || stopped[0].InstanceID != "legacy-reviewed" || stopped[1].InstanceID != "managed-removed" {
		t.Fatalf("unsafe or incomplete retirement list: %+v %v", stopped, err)
	}
	var out bytes.Buffer
	if err := printRegistryRetirements(&out, scope.Owner, stopped); err != nil {
		t.Fatal(err)
	}
	for _, wanted := range []string{"legacy-reviewed", "managed-removed", "retired cluster=core node=old tcp:9000"} {
		if !strings.Contains(out.String(), wanted) {
			t.Fatalf("preview omitted %s: %s", wanted, out.String())
		}
	}
	scope.Adopt[0].Port = 9001
	if _, err := registryRetirementPreview(desired, rows); err == nil {
		t.Fatal("changed adoption tuple was accepted")
	}
	scope.Adopt[0].Port = 9000
	rows[2].Source = "runtime"
	if _, err := registryRetirementPreview(desired, rows); err == nil {
		t.Fatal("runtime registration adopted for retirement")
	}
}

func TestRegistryRetirementPreviewIncludesDuplicateRowsAndRefusesOwnershipCollision(t *testing.T) {
	now, earlier := time.Now(), time.Now().Add(-time.Hour)
	desired := bootstrap.QuartermasterSection{RegistryScope: &bootstrap.ServiceRegistryScope{Owner: "production", Clusters: []string{"core"}, Complete: true}, ServiceRegistry: []bootstrap.ServiceRegistryEntry{{ServiceName: "db", ClusterID: "core", NodeID: "node", Protocol: "tcp", Port: 9000}}}
	rows := []registryPreviewRow{
		{InstanceID: "newest", ServiceName: "db", ClusterID: "core", NodeID: "node", Protocol: "tcp", Port: 9000, Source: "manifest", Owner: "production", Status: "running", UpdatedAt: &now},
		{InstanceID: "duplicate", ServiceName: "db", ClusterID: "core", NodeID: "node", Protocol: "tcp", Port: 9000, Source: "manifest", Owner: "production", Status: "running", UpdatedAt: &earlier},
		{InstanceID: "null-status", ServiceName: "retired", ClusterID: "core", Source: "manifest", Owner: "production", NullStatus: true},
	}
	stopped, err := registryRetirementPreview(desired, rows)
	if err != nil || len(stopped) != 1 || stopped[0].InstanceID != "duplicate" {
		t.Fatalf("preview differs from instance-ID retirement: %+v %v", stopped, err)
	}
	for _, source := range []string{"runtime", "manifest"} {
		rows[0].Source, rows[0].Owner = source, "another-owner"
		if _, collisionErr := registryRetirementPreview(desired, rows); collisionErr == nil {
			t.Fatalf("preview accepted declared tuple held by %s", source)
		}
	}
	rows[0].Source, rows[0].Owner = "manifest", "production"
	rows[1].Source, rows[1].Owner = "legacy", ""
	desired.RegistryScope.Adopt = []bootstrap.ServiceRegistryAdoption{{InstanceID: "duplicate", ServiceName: "db", ClusterID: "core", NodeID: "node", Protocol: "tcp", Port: 9000}}
	stopped, err = registryRetirementPreview(desired, rows)
	if err != nil || len(stopped) != 1 || stopped[0].InstanceID != "newest" {
		t.Fatalf("preview ignored adoption's update before tuple selection: %+v %v", stopped, err)
	}
}

func TestReleaseRefusesAnUnreadableRegistryRetirementPreview(t *testing.T) {
	previous := releaseRegistryInstancesFn
	t.Cleanup(func() { releaseRegistryInstancesFn = previous })
	releaseRegistryInstancesFn = func(context.Context, *cobra.Command, *resolvedCluster, *ssh.Pool, *bootstrap.ServiceRegistryScope) ([]registryPreviewRow, error) {
		return nil, errors.New("database unavailable")
	}
	cmd := &cobra.Command{}
	err := writeRegistryRetirementPreview(t.Context(), cmd, &resolvedCluster{}, nil, bootstrap.QuartermasterSection{RegistryScope: &bootstrap.ServiceRegistryScope{Owner: "production", Complete: true}})
	if err == nil || !strings.Contains(err.Error(), "preview incomplete") {
		t.Fatalf("unreadable inventory passed as an empty retirement: %v", err)
	}
}
