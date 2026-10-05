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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type refusingClipOwner struct {
	foghornpb.UnimplementedClipControlServiceServer
	err error
}

func (o *refusingClipOwner) DeleteClip(context.Context, *sharedpb.DeleteClipRequest) (*sharedpb.DeleteClipResponse, error) {
	return nil, o.err
}

// A Foghorn refusal of the caller's request (InvalidArgument, NotFound, FailedPrecondition) reaches the caller with
// its code and is logged below error; only a Foghorn fault (Internal) logs at error. The call goes through the real
// Foghorn gRPC client against a gRPC server that answers with the status under test.
func TestFoghornArtifactCallLogsByStatusClass(t *testing.T) {
	cases := []struct {
		code codes.Code
		want logrus.Level
	}{
		{codes.InvalidArgument, logrus.InfoLevel},
		{codes.NotFound, logrus.InfoLevel},
		{codes.FailedPrecondition, logrus.InfoLevel},
		{codes.Unavailable, logrus.WarnLevel},
		{codes.Internal, logrus.ErrorLevel},
	}
	for _, tc := range cases {
		t.Run(tc.code.String(), func(t *testing.T) {
			s, mock, closeDB := newMockServer(t)
			t.Cleanup(closeDB)
			logger, logs := logtest.NewNullLogger()
			logger.SetLevel(logrus.DebugLevel)
			s.logger = logger
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			rpc := grpc.NewServer()
			foghornpb.RegisterClipControlServiceServer(rpc, &refusingClipOwner{err: status.Error(tc.code, "refused")})
			go func() { _ = rpc.Serve(listener) }()
			t.Cleanup(func() { rpc.Stop(); _ = listener.Close() })
			pool := foghornclient.NewPool(foghornclient.PoolConfig{AllowInsecure: true, Timeout: time.Second, Logger: logrus.New()})
			t.Cleanup(func() { _ = pool.Close() })
			s.foghornPool, s.routeCacheTTL = pool, time.Minute
			s.routeCache = map[string]*clusterRoute{"tenant": {
				clusterID: "primary", foghornAddr: "unreachable.invalid:1", resolvedAt: time.Now(),
				clusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "owner", FoghornGrpcAddr: listener.Addr().String()}},
			}}
			mock.ExpectQuery("FROM commodore.clips").WithArgs("hash", "tenant").
				WillReturnRows(sqlmock.NewRows([]string{"stream_id", "origin_cluster_id"}).AddRow("stream", "owner"))

			_, err = s.DeleteClip(ctxAs("user-7", "tenant", "owner"), &sharedpb.DeleteClipRequest{ClipHash: "hash"})
			if status.Code(err) != tc.code {
				t.Fatalf("caller got %v, want code %s", err, tc.code)
			}
			var found bool
			for _, entry := range logs.AllEntries() {
				if entry.Message != "Failed to delete clip via Foghorn" {
					continue
				}
				found = true
				if entry.Level != tc.want {
					t.Fatalf("Foghorn %s logged at %s, want %s", tc.code, entry.Level, tc.want)
				}
			}
			if !found {
				t.Fatal("the failed Foghorn call was not logged")
			}
		})
	}
}
