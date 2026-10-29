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
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
)

type clipOriginOwner struct {
	foghornpb.UnimplementedClipControlServiceServer
	requests chan *sharedpb.CreateClipRequest
}

func (o *clipOriginOwner) CreateClip(_ context.Context, req *sharedpb.CreateClipRequest) (*sharedpb.CreateClipResponse, error) {
	o.requests <- req
	// Missing fulfilled timing keeps catalog convergence deferred; this test
	// exercises the durable intent and actual cross-service create request.
	return &sharedpb.CreateClipResponse{ClipHash: req.GetClipHash()}, nil
}

func TestCreateClipForwardsResolvedOriginToPooledFoghorn(t *testing.T) {
	const tenant = "10000000-0000-4000-8000-000000000001"
	const stream = "30000000-0000-4000-8000-000000000001"
	for _, tc := range []struct {
		name, wantCluster string
		activeAt          time.Time
	}{
		{name: "active private ingest", wantCluster: "private-edge", activeAt: time.Now()},
		{name: "stale ingest uses tenant route", wantCluster: "platform-cell", activeAt: time.Now().Add(-time.Hour)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, closeDB := newMockServer(t)
			t.Cleanup(closeDB)
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			owner := &clipOriginOwner{requests: make(chan *sharedpb.CreateClipRequest, 1)}
			rpc := grpc.NewServer()
			foghornpb.RegisterClipControlServiceServer(rpc, owner)
			go func() { _ = rpc.Serve(listener) }()
			t.Cleanup(func() { rpc.Stop(); _ = listener.Close() })
			pool := foghornclient.NewPool(foghornclient.PoolConfig{AllowInsecure: true, Timeout: time.Second, Logger: logrus.New()})
			t.Cleanup(func() { _ = pool.Close() })
			s.foghornPool, s.routeCacheTTL = pool, time.Minute
			// Both logical clusters use the same physical control service.
			s.routeCache = map[string]*clusterRoute{tenant: {
				clusterID: "platform-cell", foghornAddr: listener.Addr().String(), resolvedAt: time.Now(),
				clusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "private-edge", FoghornGrpcAddr: listener.Addr().String()}},
			}}
			mock.ExpectQuery("GetClipSourceStream").WithArgs(stream, tenant).
				WillReturnRows(sqlmock.NewRows([]string{"internal_name", "active_ingest_cluster_id", "active_ingest_cluster_updated_at", "requires_auth", "playback_policy", "playback_webhook_secret_enc"}).
					AddRow("stream-internal", "private-edge", tc.activeAt, false, "public", nil))
			for range 2 {
				mock.ExpectQuery("ArtifactIdentifierExists").WithArgs(sqlmock.AnyArg()).
					WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			}
			mock.ExpectQuery("GetStreamRetentionOverrides").WithArgs(stream, tenant).
				WillReturnRows(sqlmock.NewRows([]string{"stream_id", "dvr_retention_days_override", "clip_retention_days_override"}).AddRow(stream, nil, 30))
			mock.ExpectQuery("UpsertArtifactCreationIntent").
				WithArgs(tenant, "clip", sqlmock.AnyArg(), sqlmock.AnyArg(), tc.wantCluster, sqlmock.AnyArg()).
				WillReturnRows(sqlmock.NewRows([]string{"request_id"}).AddRow("request-1"))

			_, err = s.CreateClip(ctxAs("user-1", tenant, "owner"), &sharedpb.CreateClipRequest{
				StreamId: proto.String(stream), ClusterId: "untrusted-caller-cluster",
				Mode: sharedpb.ClipMode_CLIP_MODE_DURATION, StartUnix: proto.Int64(1700000000), DurationSec: proto.Int64(6),
			})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-owner.requests:
				if got.GetClusterId() != tc.wantCluster || got.GetTenantId() != tenant || got.GetRequestId() != "request-1" {
					t.Fatalf("outbound clip identity cluster=%q tenant=%q request=%q, want %q/%q/request-1", got.GetClusterId(), got.GetTenantId(), got.GetRequestId(), tc.wantCluster, tenant)
				}
			default:
				t.Fatal("the pooled Foghorn did not receive the clip request")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
