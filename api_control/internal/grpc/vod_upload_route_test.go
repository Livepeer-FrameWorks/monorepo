package grpc

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"
)

type uploadOwnerServer struct {
	foghornpb.UnimplementedVodControlServiceServer
	calls  chan string
	actors []*commonpb.RequestActor
}

func (s *uploadOwnerServer) CompleteVodUpload(_ context.Context, req *sharedpb.CompleteVodUploadRequest) (*sharedpb.CompleteVodUploadResponse, error) {
	s.actors = append(s.actors, req.GetActor())
	s.calls <- "complete:" + req.GetTenantId() + ":" + req.GetUploadId()
	return &sharedpb.CompleteVodUploadResponse{Asset: &sharedpb.VodAssetInfo{ArtifactHash: "artifact"}}, nil
}

func (s *uploadOwnerServer) GetVodUploadStatus(_ context.Context, req *sharedpb.GetVodUploadStatusRequest) (*sharedpb.GetVodUploadStatusResponse, error) {
	s.calls <- "status:" + req.GetTenantId() + ":" + req.GetUploadId()
	return &sharedpb.GetVodUploadStatusResponse{UploadId: req.GetUploadId(), ArtifactHash: "artifact"}, nil
}

func (s *uploadOwnerServer) AbortVodUpload(_ context.Context, req *sharedpb.AbortVodUploadRequest) (*sharedpb.AbortVodUploadResponse, error) {
	s.actors = append(s.actors, req.GetActor())
	s.calls <- "abort:" + req.GetTenantId() + ":" + req.GetUploadId()
	return &sharedpb.AbortVodUploadResponse{Success: true}, nil
}

func TestVodUploadOperationsStayWithCatalogOwner(t *testing.T) {
	for _, operation := range []string{"complete", "status", "abort"} {
		t.Run(operation, func(t *testing.T) {
			runVodUploadOwnerOperation(ctxAs("user", "tenant", "owner"), t, operation)
		})
	}
}

// Completing or aborting an upload names the API token caller to the owning
// Foghorn, with the token hash computed here, so Foghorn's upload.completed
// and upload.aborted carry the caller as events.ActorFromContext records it.
func TestVodUploadOperationsNameTheCallerToTheOwner(t *testing.T) {
	for _, operation := range []string{"complete", "abort"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.WithValue(ctxAs("user-7", "tenant", "owner"), ctxkeys.KeyAuthType, "api_token")
			ctx = context.WithValue(ctx, ctxkeys.KeyAPITokenID, "token-record-9")
			owner := runVodUploadOwnerOperation(ctx, t, operation)
			want := &commonpb.RequestActor{
				AuthType: "api_token", UserId: "user-7",
				TokenHash: events.HashIdentifier([]byte("commodore-test-usage-hash-secret"), "token-record-9"),
			}
			if len(owner.actors) != 1 || !proto.Equal(owner.actors[0], want) {
				t.Fatalf("owner received actors %v, want %v", owner.actors, want)
			}
		})
	}
}

// runVodUploadOwnerOperation runs operation on an upload session through
// Commodore against a real gRPC upload owner and returns that owner.
func runVodUploadOwnerOperation(ctx context.Context, t *testing.T, operation string) *uploadOwnerServer {
	t.Helper()
	s, mock, closeDB := newMockServer(t)
	t.Cleanup(closeDB)
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	owner := &uploadOwnerServer{calls: make(chan string, 1)}
	rpc := grpc.NewServer()
	foghornpb.RegisterVodControlServiceServer(rpc, owner)
	go func() { _ = rpc.Serve(listener) }()
	t.Cleanup(func() { rpc.Stop(); _ = listener.Close() })
	pool := foghornclient.NewPool(foghornclient.PoolConfig{AllowInsecure: true, Timeout: time.Second, Logger: logrus.New()})
	t.Cleanup(func() { _ = pool.Close() })
	s.foghornPool, s.routeCacheTTL = pool, time.Minute
	s.routeCache = map[string]*clusterRoute{"tenant": {
		clusterID: "new-primary", foghornAddr: "unreachable.invalid:1", resolvedAt: time.Now(),
		clusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "upload-owner", FoghornGrpcAddr: listener.Addr().String()}},
	}}
	mock.ExpectQuery("SELECT origin_cluster_id").WithArgs("artifact", "tenant").
		WillReturnRows(sqlmock.NewRows([]string{"origin_cluster_id"}).AddRow("upload-owner"))
	session := vodUploadSessionID("artifact", "storage:upload/+=")
	switch operation {
	case "complete":
		mock.ExpectQuery("SELECT playback_id").WithArgs("tenant", "artifact").
			WillReturnRows(sqlmock.NewRows([]string{"playback_id"}).AddRow("playback"))
		var response *sharedpb.CompleteVodUploadResponse
		response, err = s.CompleteVodUpload(ctx, &sharedpb.CompleteVodUploadRequest{UploadId: session})
		if err == nil && response.GetAsset().GetPlaybackId() != "playback" {
			t.Fatal("completion did not return the catalog playback identity")
		}
	case "status":
		mock.ExpectQuery("SELECT playback_id").WithArgs("tenant", "artifact").
			WillReturnRows(sqlmock.NewRows([]string{"playback_id"}).AddRow("playback"))
		var response *sharedpb.GetVodUploadStatusResponse
		response, err = s.GetVodUploadStatus(ctx, &sharedpb.GetVodUploadStatusRequest{UploadId: session})
		if err == nil && (response.GetUploadId() != session || response.GetPlaybackId() != "playback") {
			t.Fatal("status did not preserve public session identity")
		}
	case "abort":
		_, err = s.AbortVodUpload(ctx, &sharedpb.AbortVodUploadRequest{UploadId: session})
	}
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-owner.calls:
		if got != operation+":tenant:storage:upload/+=" {
			t.Fatalf("wrong owner request: %s", got)
		}
	default:
		t.Fatal("upload owner was not contacted")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	return owner
}

func TestVodUploadRouteRejectsInvalidOrUnownedSession(t *testing.T) {
	for _, input := range []string{"", "raw-s3-upload", globalid.Encode("Stream", "artifact:upload"), vodUploadSessionID("", "upload"), vodUploadSessionID("artifact", ""), strings.Repeat("x", 16385)} {
		s, mock, done := newMockServer(t)
		_, err := s.resolveVodUploadRoute(t.Context(), "tenant", input)
		wantCode(t, err, codes.InvalidArgument)
		if err = mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
		done()
	}
	for _, tc := range []struct {
		name string
		err  error
		want codes.Code
	}{
		{"foreign or missing artifact", sql.ErrNoRows, codes.NotFound},
		{"catalog outage", errors.New("database unavailable"), codes.Unavailable},
		{"missing owner", nil, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, done := newMockServer(t)
			t.Cleanup(done)
			query := mock.ExpectQuery("SELECT origin_cluster_id").WithArgs("artifact", "tenant")
			if tc.err != nil {
				query.WillReturnError(tc.err)
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"origin_cluster_id"}).AddRow(nil))
			}
			_, err := s.resolveVodUploadRoute(t.Context(), "tenant", vodUploadSessionID("artifact", "upload"))
			wantCode(t, err, tc.want)
			if err = mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
