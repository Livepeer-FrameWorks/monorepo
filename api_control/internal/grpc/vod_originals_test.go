package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/DATA-DOG/go-sqlmock"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"net"
	"testing"
	"time"
)

func TestOriginalUploadPolicyCascade(t *testing.T) {
	for _, test := range []struct {
		name     string
		override *bool
		stored   any
		missing  bool
		want     bool
	}{
		{name: "system default", missing: true, want: true}, {name: "null inherits", want: true},
		{name: "account opts out", stored: false}, {name: "account keeps", stored: true, want: true},
		{name: "upload opts out", override: proto.Bool(false)}, {name: "upload keeps", override: proto.Bool(true), want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, mock, done := newMockServer(t)
			defer done()
			if test.override == nil {
				q := mock.ExpectQuery("GetVodOriginalPolicy").WithArgs(testTenantID)
				if test.missing {
					q.WillReturnError(sql.ErrNoRows)
				} else {
					q.WillReturnRows(sqlmock.NewRows([]string{"keep_vod_originals"}).AddRow(test.stored))
				}
			}
			got, err := server.resolveKeepVodOriginal(context.Background(), testTenantID, test.override)
			if err != nil || got != test.want {
				t.Fatalf("keep=%v want %v: %v", got, test.want, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOriginalDownloadRefusesExhaustedOrUnavailableSharedBucket(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		code codes.Code
	}{{"exhausted", sql.ErrNoRows, codes.ResourceExhausted}, {"unavailable", sql.ErrConnDone, codes.Unavailable}} {
		t.Run(test.name, func(t *testing.T) {
			server, mock, done := newMockServer(t)
			defer done()
			mock.ExpectQuery("ResolveVODTenantArtifact").WithArgs("asset-id", testTenantID).
				WillReturnRows(sqlmock.NewRows([]string{"origin_cluster_id", "vod_hash", "origin_type"}).AddRow("origin", "hash", "upload"))
			mock.ExpectQuery("ConsumeVodOriginalDownloadGrant").WithArgs(testTenantID, int32(3), int32(12)).WillReturnError(test.err)
			_, err := server.CreateVodOriginalDownload(ctxAs("u1", testTenantID, "owner"), &sharedpb.CreateVodOriginalDownloadRequest{ArtifactHash: "asset-id"})
			if status.Code(err) != test.code {
				t.Fatalf("shared bucket returned %v, want %v", err, test.code)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type originalDeletionFoghorn struct {
	foghornpb.UnimplementedVodControlServiceServer
	deleted  bool
	request  *sharedpb.DeleteVodOriginalRequest
	download *sharedpb.CreateVodOriginalDownloadRequest
}

func (f *originalDeletionFoghorn) DeleteVodOriginal(_ context.Context, req *sharedpb.DeleteVodOriginalRequest) (*sharedpb.DeleteVodOriginalResponse, error) {
	f.request = req
	return &sharedpb.DeleteVodOriginalResponse{Success: f.deleted}, nil
}

func (f *originalDeletionFoghorn) CreateVodOriginalDownload(_ context.Context, req *sharedpb.CreateVodOriginalDownloadRequest) (*sharedpb.CreateVodOriginalDownloadResponse, error) {
	f.download = req
	return &sharedpb.CreateVodOriginalDownloadResponse{Url: "https://store/original.mov", SizeBytes: 2000}, nil
}

func TestOriginalDownloadRoutesToOwnerAndRecordsGrant(t *testing.T) {
	server, mock, done := newMockServer(t)
	defer done()
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	rpc := grpc.NewServer()
	remote := &originalDeletionFoghorn{}
	foghornpb.RegisterVodControlServiceServer(rpc, remote)
	go func() { _ = rpc.Serve(listener) }()
	defer rpc.Stop()
	defer listener.Close()
	pool := foghornclient.NewPool(foghornclient.PoolConfig{AllowInsecure: true, Timeout: time.Second, Logger: logrus.New()})
	defer pool.Close()
	server.foghornPool, server.routeCacheTTL = pool, time.Minute
	server.routeCache = map[string]*clusterRoute{testTenantID: {
		clusterID: "primary", foghornAddr: "unreachable.invalid:1", resolvedAt: time.Now(),
		clusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "origin", FoghornGrpcAddr: listener.Addr().String()}},
	}}
	mock.ExpectQuery("ResolveVODTenantArtifact").WithArgs("asset-id", testTenantID).
		WillReturnRows(sqlmock.NewRows([]string{"origin_cluster_id", "vod_hash", "origin_type"}).AddRow("origin", "hash", "upload"))
	mock.ExpectQuery("ConsumeVodOriginalDownloadGrant").WithArgs(testTenantID, int32(3), int32(12)).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow(testTenantID))
	mock.ExpectBegin()
	expectLegacyEventInsert(mock, "media.vod_original.download_issued")
	mock.ExpectCommit()
	resp, err := server.CreateVodOriginalDownload(ctxAs("u1", testTenantID, "owner"), &sharedpb.CreateVodOriginalDownloadRequest{ArtifactHash: "asset-id"})
	if err != nil || resp.GetUrl() != "https://store/original.mov" {
		t.Fatalf("download=%v error=%v", resp, err)
	}
	if remote.download.GetTenantId() != testTenantID || remote.download.GetArtifactHash() != "hash" {
		t.Fatalf("owner did not receive tenant-scoped source identity: %v", remote.download)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestOriginalDeletionRoutesToOwnerAndOnlyEmitsForDeletion(t *testing.T) {
	for _, deleted := range []bool{false, true} {
		t.Run(fmt.Sprint(deleted), func(t *testing.T) {
			server, mock, done := newMockServer(t)
			defer done()
			listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			rpc := grpc.NewServer()
			remote := &originalDeletionFoghorn{deleted: deleted}
			foghornpb.RegisterVodControlServiceServer(rpc, remote)
			go func() { _ = rpc.Serve(listener) }()
			defer rpc.Stop()
			defer listener.Close()
			pool := foghornclient.NewPool(foghornclient.PoolConfig{AllowInsecure: true, Timeout: time.Second, Logger: logrus.New()})
			defer pool.Close()
			server.foghornPool, server.routeCacheTTL = pool, time.Minute
			server.routeCache = map[string]*clusterRoute{testTenantID: {
				clusterID: "primary", foghornAddr: "unreachable.invalid:1", resolvedAt: time.Now(),
				clusterPeers: []*clusterpeerpb.TenantClusterPeer{{ClusterId: "origin", FoghornGrpcAddr: listener.Addr().String()}},
			}}
			mock.ExpectQuery("ResolveVODTenantArtifact").WithArgs("asset-id", testTenantID).WillReturnRows(sqlmock.NewRows([]string{"origin_cluster_id", "vod_hash", "origin_type"}).AddRow("origin", "hash", "upload"))
			if deleted {
				mock.ExpectBegin()
				expectLegacyEventInsert(mock, "media.vod_original.deleted")
				mock.ExpectCommit()
			}
			resp, err := server.DeleteVodOriginal(ctxAs("u1", testTenantID, "owner"), &sharedpb.DeleteVodOriginalRequest{ArtifactHash: "asset-id"})
			if err != nil || resp.GetSuccess() != deleted {
				t.Fatalf("response=%v error=%v", resp, err)
			}
			if remote.request.GetTenantId() != testTenantID || remote.request.GetArtifactHash() != "hash" {
				t.Fatalf("owner routing lost identity: %v", remote.request)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOriginalOperationsRequireTenantOwnership(t *testing.T) {
	server, mock, done := newMockServer(t)
	defer done()
	for _, action := range []func(context.Context, string, string) (string, *assetRetentionTarget, error){server.originalTarget} {
		if _, _, err := action(context.Background(), "", "asset"); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("anonymous original access: %v", err)
		}
		if _, _, err := action(ctxAs("u1", testTenantID, "owner"), "other-tenant", "asset"); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("cross-tenant original access: %v", err)
		}
		mock.ExpectQuery("ResolveVODTenantArtifact").WithArgs("foreign-hash", testTenantID).WillReturnError(sql.ErrNoRows)
		if _, _, err := action(ctxAs("u1", testTenantID, "owner"), "", "foreign-hash"); status.Code(err) != codes.NotFound {
			t.Fatalf("foreign original disclosed: %v", err)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
