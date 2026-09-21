package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
	"google.golang.org/grpc"
)

type artifactDeletionOwner struct {
	foghornpb.UnimplementedClipControlServiceServer
	foghornpb.UnimplementedDVRControlServiceServer
	foghornpb.UnimplementedVodControlServiceServer
	requestedBy chan string
}

func (o *artifactDeletionOwner) DeleteClip(_ context.Context, req *sharedpb.DeleteClipRequest) (*sharedpb.DeleteClipResponse, error) {
	o.requestedBy <- req.GetRequestedByUserId()
	return &sharedpb.DeleteClipResponse{Success: true}, nil
}

func (o *artifactDeletionOwner) DeleteDVR(_ context.Context, req *sharedpb.DeleteDVRRequest) (*sharedpb.DeleteDVRResponse, error) {
	o.requestedBy <- req.GetRequestedByUserId()
	return &sharedpb.DeleteDVRResponse{Success: true}, nil
}

func (o *artifactDeletionOwner) DeleteVodAsset(_ context.Context, req *sharedpb.DeleteVodAssetRequest) (*sharedpb.DeleteVodAssetResponse, error) {
	o.requestedBy <- req.GetRequestedByUserId()
	return &sharedpb.DeleteVodAssetResponse{Success: true}, nil
}

// Foghorn commits artifact_deleted in its deletion transaction, so a
// confirmed deletion writes nothing to Commodore's outboxes: the mock
// database accepts only the routing lookup, and any transaction or insert
// fails the test. The requester travels on the request for that event.
func TestArtifactDeletionNamesRequesterAndRecordsNoEvent(t *testing.T) {
	for _, kind := range []string{"clip", "dvr", "vod"} {
		t.Run(kind, func(t *testing.T) {
			s, mock, closeDB := newMockServer(t)
			t.Cleanup(closeDB)
			// A statement the mock does not expect fails without failing the
			// handler, which only logs a failed event write; the hook catches it.
			logger, logs := logtest.NewNullLogger()
			s.logger = logger
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			owner := &artifactDeletionOwner{requestedBy: make(chan string, 1)}
			rpc := grpc.NewServer()
			foghornpb.RegisterClipControlServiceServer(rpc, owner)
			foghornpb.RegisterDVRControlServiceServer(rpc, owner)
			foghornpb.RegisterVodControlServiceServer(rpc, owner)
			go func() { _ = rpc.Serve(listener) }()
			t.Cleanup(func() { rpc.Stop(); _ = listener.Close() })
			pool := foghornclient.NewPool(foghornclient.PoolConfig{AllowInsecure: true, Timeout: time.Second, Logger: logrus.New()})
			t.Cleanup(func() { _ = pool.Close() })
			s.foghornPool, s.routeCacheTTL = pool, time.Minute
			s.routeCache = map[string]*clusterRoute{"tenant": {
				clusterID: "primary", foghornAddr: "unreachable.invalid:1", resolvedAt: time.Now(),
				clusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "owner", FoghornGrpcAddr: listener.Addr().String()}},
			}}
			ctx := ctxAs("user-7", "tenant", "owner")

			switch kind {
			case "clip":
				mock.ExpectQuery("FROM commodore.clips").WithArgs("hash", "tenant").
					WillReturnRows(sqlmock.NewRows([]string{"stream_id", "origin_cluster_id"}).AddRow("stream", "owner"))
				_, err = s.DeleteClip(ctx, &sharedpb.DeleteClipRequest{ClipHash: "hash"})
			case "dvr":
				mock.ExpectQuery("FROM commodore.dvr_recordings").WithArgs("hash", "tenant").
					WillReturnRows(sqlmock.NewRows([]string{"stream_id", "origin_cluster_id"}).AddRow("stream", "owner"))
				_, err = s.DeleteDVR(ctx, &sharedpb.DeleteDVRRequest{DvrHash: "hash"})
			case "vod":
				mock.ExpectQuery("SELECT origin_cluster_id").WithArgs("hash", "tenant").
					WillReturnRows(sqlmock.NewRows([]string{"origin_cluster_id"}).AddRow("owner"))
				_, err = s.DeleteVodAsset(ctx, &sharedpb.DeleteVodAssetRequest{ArtifactHash: "hash"})
			}
			if err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-owner.requestedBy:
				if got != "user-7" {
					t.Fatalf("Foghorn got requester %q, want user-7", got)
				}
			default:
				t.Fatal("the owning Foghorn was not asked to delete")
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			for _, entry := range logs.AllEntries() {
				if entry.Level <= logrus.ErrorLevel {
					t.Fatalf("deletion logged an error: %s (%v)", entry.Message, entry.Data)
				}
			}
		})
	}
}
