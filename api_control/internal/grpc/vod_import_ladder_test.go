package grpc

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
)

type importLadderOwner struct {
	foghornpb.UnimplementedVodControlServiceServer
	requests chan *sharedpb.ImportVodAssetRequest
}

func (f *importLadderOwner) ImportVodAsset(_ context.Context, req *sharedpb.ImportVodAssetRequest) (*sharedpb.ImportVodAssetResponse, error) {
	f.requests <- req
	return &sharedpb.ImportVodAssetResponse{Asset: &sharedpb.VodAssetInfo{ArtifactHash: req.GetVodHash()}}, nil
}

func TestImportLadderReachesOwnerAndCatalog(t *testing.T) {
	for _, choice := range []string{"uhd", ""} {
		t.Run("choice="+choice, func(t *testing.T) {
			server, mock, done := newMockServer(t)
			defer done()
			server.purserClient = startPurserEntitlementFake(t, ladderPurser(t, ladderTier(true)))
			owner := connectImportLadderOwner(t, server)
			if choice == "" {
				expectTenantLadder(mock, "standard")
			}
			for range 2 {
				mock.ExpectQuery("ArtifactIdentifierExists").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			}
			mock.ExpectQuery("SELECT default_vod_retention_days").WillReturnError(sql.ErrNoRows)
			mock.ExpectBegin()
			args := make([]driver.Value, 14)
			for i := range args {
				args[i] = sqlmock.AnyArg()
			}
			args[1], args[2], args[8], args[13] = testTenantID, "u1", "video.mp4", choice
			mock.ExpectExec("InsertVODUploadRegistration").WithArgs(args...).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectQuery("UpsertArtifactCreationIntent").WillReturnRows(sqlmock.NewRows([]string{"request_id"}).AddRow("import-request"))
			mock.ExpectCommit()
			mock.ExpectBegin()
			mock.ExpectExec("TerminalizeArtifactCreationIntent").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()
			resp, err := server.ImportVodAsset(ctxAs("u1", testTenantID, "owner"), &sharedpb.ImportVodAssetRequest{
				SourceUrl: "https://import.example/video.mp4", TranscodeLadder: choice,
			})
			if err != nil {
				t.Fatal(err)
			}
			request := <-owner.requests
			want := "720p,1080p"
			if choice == "uhd" {
				want += ",2160p"
			}
			if got := strings.Join(renditionNames(t, request.GetProcessesJson()), ","); got != want {
				t.Fatalf("import owner received ladder %q, want %q", got, want)
			}
			if request.GetTenantId() != testTenantID || request.GetRequestId() != "import-request" || request.GetVodHash() != resp.GetAsset().GetArtifactHash() {
				t.Fatalf("import lost its catalog identity or creation intent: %v", request)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func connectImportLadderOwner(t *testing.T, server *CommodoreServer) *importLadderOwner {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	owner := &importLadderOwner{requests: make(chan *sharedpb.ImportVodAssetRequest, 1)}
	rpc := grpc.NewServer()
	foghornpb.RegisterVodControlServiceServer(rpc, owner)
	go func() { _ = rpc.Serve(listener) }()
	t.Cleanup(func() { rpc.Stop(); _ = listener.Close() })
	pool := foghornclient.NewPool(foghornclient.PoolConfig{AllowInsecure: true, Timeout: time.Second, Logger: logrus.New()})
	t.Cleanup(func() { _ = pool.Close() })
	server.foghornPool, server.routeCacheTTL = pool, time.Minute
	server.routeCache = map[string]*clusterRoute{testTenantID: {
		clusterID: "origin", foghornAddr: listener.Addr().String(), resolvedAt: time.Now(),
	}}
	server.importDestinationPolicy = restream.PublicDestinationPolicy()
	server.importDestinationPolicy.LookupIP = func(context.Context, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("93.184.216.34")}, nil
	}
	return owner
}
