package grpc

import (
	"context"
	"testing"

	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

// An artifact resolve without peers makes Foghorn refuse every cross-cell
// origin, so the route failure behind it must be recorded with its cause.
func TestPopulateArtifactClusterContextLogsRouteFailure(t *testing.T) {
	logger, hook := logtest.NewNullLogger()
	server := &CommodoreServer{logger: logger}

	var peers, authority []*clusterpeerpb.TenantClusterPeer
	server.populateArtifactClusterContext(context.Background(), "tenant-1", &peers)
	server.populateArtifactAuthorityClusterContext(context.Background(), "tenant-1", &authority)

	if len(peers) != 0 || len(authority) != 0 {
		t.Fatalf("peers populated without a route: %v / %v", peers, authority)
	}
	entries := hook.AllEntries()
	if len(entries) != 2 {
		t.Fatalf("expected one warning per swallowed route failure, got %d", len(entries))
	}
	for _, entry := range entries {
		if entry.Level != logrus.WarnLevel || entry.Data["tenant_id"] != "tenant-1" || entry.Data[logrus.ErrorKey] == nil {
			t.Fatalf("warning lacks level, tenant or cause: %+v", entry)
		}
	}
}
