package grpc

import (
	"database/sql"
	"net"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestDVRStartRoutesToActiveIngestNotPrimary(t *testing.T) {
	s, mock, closeDB := newMockServer(t)
	t.Cleanup(closeDB)
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpc := grpc.NewServer()
	go func() { _ = rpc.Serve(listener) }()
	t.Cleanup(func() { rpc.Stop(); _ = listener.Close() })
	pool := foghornclient.NewPool(foghornclient.PoolConfig{AllowInsecure: true, Timeout: time.Second, Logger: logrus.New()})
	t.Cleanup(func() { _ = pool.Close() })
	s.foghornPool, s.routeCacheTTL = pool, time.Minute
	s.routeCache = map[string]*clusterRoute{"tenant": {
		clusterID: "primary", foghornAddr: "unreachable.invalid:1", resolvedAt: time.Now(),
		clusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "ingest", FoghornGrpcAddr: listener.Addr().String()}},
	}}
	mock.ExpectQuery("-- name: GetDVRSourceRoute").WithArgs("stream", "tenant").WillReturnRows(sqlmock.NewRows([]string{"cluster", "updated"}).AddRow("ingest", time.Now()))
	client, cluster, err := s.resolveDVRStartOwner(t.Context(), "tenant", "stream")
	if err != nil || client == nil || cluster != "ingest" {
		t.Fatalf("recording routed away from ingest: client=%v cluster=%q err=%v", client != nil, cluster, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestDVRStartRequiresTenantOwnedFreshSource(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cluster any
		updated any
		dbErr   error
		code    codes.Code
	}{
		{"foreign or absent", nil, nil, sql.ErrNoRows, codes.NotFound},
		{"authority unavailable", nil, nil, sql.ErrConnDone, codes.Unavailable},
		{"no lease", nil, nil, nil, codes.FailedPrecondition},
		{"stale lease", "ingest", time.Now().Add(-24 * time.Hour), nil, codes.FailedPrecondition},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, closeDB := newMockServer(t)
			t.Cleanup(closeDB)
			query := mock.ExpectQuery("-- name: GetDVRSourceRoute").WithArgs("stream", "tenant")
			if tc.dbErr != nil {
				query.WillReturnError(tc.dbErr)
			} else {
				query.WillReturnRows(sqlmock.NewRows([]string{"cluster", "updated"}).AddRow(tc.cluster, tc.updated))
			}
			client, _, err := s.resolveDVRStartOwner(t.Context(), "tenant", "stream")
			if client != nil || status.Code(err) != tc.code {
				t.Fatalf("got %v; want %v", err, tc.code)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
