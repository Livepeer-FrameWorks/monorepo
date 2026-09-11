package grpc

import (
	"context"
	"database/sql"
	"net"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type placementViewerProxyServer struct {
	foghornpb.UnimplementedViewerControlServiceServer
	resolve func(context.Context, *sharedpb.ViewerEndpointRequest) (*sharedpb.ViewerEndpointResponse, error)
}

func (server *placementViewerProxyServer) ResolveViewerEndpoint(ctx context.Context, req *sharedpb.ViewerEndpointRequest) (*sharedpb.ViewerEndpointResponse, error) {
	return server.resolve(ctx, req)
}

func TestPreparedViewerProxyRoutesByContentNotViewerTenant(t *testing.T) {
	for _, viewerTenant := range []string{"", "owner", "different-viewer"} {
		t.Run("viewer="+viewerTenant, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			mock.ExpectQuery("-- name: NormalizeArtifactPlaybackID").WithArgs("public").WillReturnError(sql.ErrNoRows)
			mock.ExpectQuery("-- name: GetStreamRouteByPlaybackID").WithArgs("public").WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "active_ingest_cluster_id"}).AddRow("owner", "eu"))
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			calls := make(chan *sharedpb.ViewerEndpointRequest, 1)
			rpc := grpc.NewServer(grpc.UnaryInterceptor(middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{ServiceToken: "placement-service", MetadataPolicy: middleware.MetadataPolicyDeny})))
			foghornpb.RegisterViewerControlServiceServer(rpc, &placementViewerProxyServer{resolve: func(ctx context.Context, req *sharedpb.ViewerEndpointRequest) (*sharedpb.ViewerEndpointResponse, error) {
				md, _ := metadata.FromIncomingContext(ctx)
				if len(md.Get("x-payment")) != 1 || md.Get("x-payment")[0] != "payment-proof" || len(md.Get("payment-signature")) != 1 || md.Get("payment-signature")[0] != "payment-signature" {
					t.Error("viewer payment context lost during authenticated proxy")
				}
				calls <- req
				return &sharedpb.ViewerEndpointResponse{Primary: &sharedpb.ViewerEndpoint{NodeId: "us-edge", ClusterId: "us", Protocol: "whep", Url: "https://us.example/whep/public", BaseUrl: "https://us.example"}}, nil
			}})
			go func() { _ = rpc.Serve(listener) }()
			t.Cleanup(func() { rpc.Stop(); _ = listener.Close() })
			pool := foghornclient.NewPool(foghornclient.PoolConfig{ServiceToken: "placement-service", AllowInsecure: true, Timeout: time.Second, Logger: logrus.New()})
			t.Cleanup(func() { _ = pool.Close() })
			server := &CommodoreServer{db: db, logger: logrus.New(), foghornPool: pool, routeCacheTTL: time.Minute, routeCache: map[string]*clusterRoute{
				"owner":            {clusterID: "eu", foghornAddr: listener.Addr().String(), resolvedAt: time.Now()},
				"different-viewer": {clusterID: "wrong-viewer-cluster", foghornAddr: "unreachable.invalid:1", resolvedAt: time.Now()},
			}}
			ctx := context.WithValue(t.Context(), ctxkeys.KeyTenantID, viewerTenant)
			ctx = metadata.NewIncomingContext(ctx, metadata.Pairs("x-payment", "payment-proof", "payment-signature", "payment-signature"))
			ip, token := "192.0.2.7", "viewer-token"
			response, err := server.ResolveViewerEndpoint(ctx, &sharedpb.ViewerEndpointRequest{ContentId: "public", Protocol: "whep", ViewerIp: &ip, ViewerToken: &token})
			if err != nil || response.GetPrimary().GetNodeId() != "us-edge" || response.GetPrimary().GetClusterId() != "us" || response.GetPrimary().GetUrl() != "https://us.example/whep/public" || len(response.GetFallbacks()) != 0 {
				t.Fatalf("content coordinator/destination changed: %+v, %v", response, err)
			}
			select {
			case req := <-calls:
				if req.Protocol != "whep" || req.ContentId != "public" || req.GetViewerIp() != ip || req.GetViewerToken() != token {
					t.Fatalf("proxy lost viewer request: %+v", req)
				}
			default:
				t.Fatal("content owner's coordinator was not contacted")
			}
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestPreparedViewerProxyDoesNotUseViewerRouteWithoutContentAuthority(t *testing.T) {
	server := &CommodoreServer{logger: logrus.New(), routeCacheTTL: time.Minute, routeCache: map[string]*clusterRoute{
		"viewer": {clusterID: "viewer-cluster", foghornAddr: "unused.invalid:1", resolvedAt: time.Now()},
	}}
	ctx := context.WithValue(t.Context(), ctxkeys.KeyTenantID, "viewer")
	response, err := server.ResolveViewerEndpoint(ctx, &sharedpb.ViewerEndpointRequest{ContentId: "public", Protocol: "hls"})
	if status.Code(err) != codes.Unavailable || response != nil {
		t.Fatalf("missing content authority returned a viewer-owned route: %+v, %v", response, err)
	}
}
