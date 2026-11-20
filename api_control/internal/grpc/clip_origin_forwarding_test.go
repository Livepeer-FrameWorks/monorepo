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
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
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

type clipOriginChapterReader struct {
	foghornpb.UnimplementedDVRControlServiceServer
	requests chan *foghorncontrolpb.ListDVRChaptersRequest
	chapter  *foghorncontrolpb.ChapterRef
	legacy   bool
}

func (r *clipOriginChapterReader) ListDVRChapters(ctx context.Context, req *foghorncontrolpb.ListDVRChaptersRequest) (*foghorncontrolpb.ListDVRChaptersResponse, error) {
	r.requests <- req
	response := &foghorncontrolpb.ListDVRChaptersResponse{}
	if r.legacy {
		return response, nil
	}
	grpc.SetTrailer(ctx, metadata.Pairs(foghorncontrolpb.ArchivedClipCoverageTrailer, "1"))
	if r.chapter != nil {
		response.Chapters = []*foghorncontrolpb.ChapterRef{r.chapter}
	}
	return response, nil
}

type clipOriginArchive struct {
	dvrHash, cluster, catalogHash string
	chapter                       *foghorncontrolpb.ChapterRef
}

func TestCreateClipForwardsResolvedOriginToPooledFoghorn(t *testing.T) {
	const tenant = "10000000-0000-4000-8000-000000000001"
	const stream = "30000000-0000-4000-8000-000000000001"
	const startUnix, stopUnix int64 = 1700000000, 1700000004
	const startMs, stopMs = startUnix * 1000, stopUnix * 1000

	reclaimedArchive := clipOriginArchive{
		dvrHash: "archive-dvr-a", cluster: "archive-a", catalogHash: "verified-chapter-a",
		chapter: &foghorncontrolpb.ChapterRef{
			State: "reclaimed", PlaybackArtifactHash: "verified-chapter-a",
			StartMs: startMs - 4000, EndMs: stopMs + 4000,
			ActualMediaStartMs: startMs - 1000, ActualMediaEndMs: stopMs,
		},
	}
	differentArchive := clipOriginArchive{
		dvrHash: "archive-dvr-b", cluster: "archive-b", catalogHash: "verified-chapter-b",
		chapter: &foghorncontrolpb.ChapterRef{
			State: "finalized", PlaybackArtifactHash: "verified-chapter-b",
			StartMs: startMs - 4000, EndMs: stopMs + 4000,
			ActualMediaStartMs: startMs, ActualMediaEndMs: stopMs + 1000,
		},
	}
	wrongCatalogArchive := differentArchive
	wrongCatalogArchive.catalogHash = "another-chapter"
	partialArchive := differentArchive
	partialArchive.chapter = &foghorncontrolpb.ChapterRef{
		State: "frozen", PlaybackArtifactHash: "verified-chapter-b",
		StartMs: startMs - 4000, EndMs: stopMs + 4000,
		ActualMediaStartMs: startMs, ActualMediaEndMs: stopMs - 1000,
	}
	for _, tc := range []struct {
		name, wantCluster string
		activeAt          any
		archives          []clipOriginArchive
		absolute          bool
		legacyCluster     string
		wantErr           codes.Code
	}{
		{name: "active private ingest", wantCluster: "private-edge", activeAt: time.Now()},
		{
			name: "stale ingest with no registered chapter uses tenant route", wantCluster: "platform-cell",
			activeAt: time.Now().Add(-time.Hour), archives: []clipOriginArchive{},
		},
		{
			name: "stale ingest with no covering archive uses tenant route", wantCluster: "platform-cell",
			activeAt: time.Now().Add(-time.Hour), archives: []clipOriginArchive{partialArchive},
		},
		{
			name: "stale ingest routes reclaimed chapter to its archive cell", wantCluster: "archive-a",
			activeAt: time.Now().Add(-time.Hour), archives: []clipOriginArchive{wrongCatalogArchive, reclaimedArchive},
		},
		{
			name: "absent ingest and multiple covering archives reject before create", absolute: true,
			archives: []clipOriginArchive{reclaimedArchive, differentArchive}, wantErr: codes.FailedPrecondition,
		},
		{
			name: "legacy archive origin rejects before create", activeAt: time.Now().Add(-time.Hour),
			archives: []clipOriginArchive{reclaimedArchive}, legacyCluster: reclaimedArchive.cluster, wantErr: codes.Unavailable,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, closeDB := newMockServer(t)
			t.Cleanup(closeDB)
			owners := make(map[string]*clipOriginOwner)
			readers := make(map[string]*clipOriginChapterReader)
			expectedReads := make(map[string]clipOriginArchive)
			for _, archive := range tc.archives {
				expectedReads[archive.cluster] = archive
			}
			route := &clusterRoute{clusterID: "platform-cell", resolvedAt: time.Now()}
			for _, cluster := range []string{"platform-cell", "private-edge", "archive-a", "archive-b", "untrusted-caller-cluster"} {
				listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				owner := &clipOriginOwner{requests: make(chan *sharedpb.CreateClipRequest, 8)}
				reader := &clipOriginChapterReader{
					requests: make(chan *foghorncontrolpb.ListDVRChaptersRequest, 8),
					chapter:  expectedReads[cluster].chapter,
					legacy:   tc.legacyCluster == cluster,
				}
				owners[cluster], readers[cluster] = owner, reader
				rpc := grpc.NewServer()
				foghornpb.RegisterClipControlServiceServer(rpc, owner)
				foghornpb.RegisterDVRControlServiceServer(rpc, reader)
				go func() { _ = rpc.Serve(listener) }()
				t.Cleanup(func() { rpc.Stop(); _ = listener.Close() })
				if cluster == route.clusterID {
					route.foghornAddr = listener.Addr().String()
				} else {
					route.clusterPeers = append(route.clusterPeers, &clusterpeerpb.TenantClusterPeer{
						ClusterId: cluster, FoghornGrpcAddr: listener.Addr().String(),
					})
				}
			}
			pool := foghornclient.NewPool(foghornclient.PoolConfig{AllowInsecure: true, Timeout: time.Second, Logger: logrus.New()})
			t.Cleanup(func() { _ = pool.Close() })
			s.foghornPool, s.routeCacheTTL = pool, time.Minute
			s.routeCache = map[string]*clusterRoute{tenant: route}
			var activeCluster any = "private-edge"
			if tc.activeAt == nil {
				activeCluster = nil
			}
			mock.ExpectQuery("GetClipSourceStream").WithArgs(stream, tenant).
				WillReturnRows(sqlmock.NewRows([]string{"internal_name", "active_ingest_cluster_id", "active_ingest_cluster_updated_at", "requires_auth", "playback_policy", "playback_webhook_secret_enc"}).
					AddRow("stream-internal", activeCluster, tc.activeAt, false, "public", nil))
			if tc.archives != nil {
				rows := sqlmock.NewRows([]string{"dvr_hash", "origin_cluster_id", "artifact_hash"})
				for _, archive := range tc.archives {
					rows.AddRow(archive.dvrHash, archive.cluster, archive.catalogHash)
				}
				mock.ExpectQuery("FindArchivedClipDVRs").WithArgs(tenant, stream, "stream-internal").WillReturnRows(rows)
			}
			if tc.wantCluster != "" {
				for range 2 {
					mock.ExpectQuery("ArtifactIdentifierExists").WithArgs(sqlmock.AnyArg()).
						WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
				}
				mock.ExpectQuery("GetStreamRetentionOverrides").WithArgs(stream, tenant).
					WillReturnRows(sqlmock.NewRows([]string{"stream_id", "dvr_retention_days_override", "clip_retention_days_override"}).AddRow(stream, nil, 30))
				mock.ExpectQuery("UpsertArtifactCreationIntent").
					WithArgs(tenant, "clip", sqlmock.AnyArg(), sqlmock.AnyArg(), tc.wantCluster, sqlmock.AnyArg()).
					WillReturnRows(sqlmock.NewRows([]string{"request_id"}).AddRow("request-1"))
			}

			request := &sharedpb.CreateClipRequest{
				StreamId: proto.String(stream), ClusterId: "untrusted-caller-cluster",
				Mode: sharedpb.ClipMode_CLIP_MODE_DURATION, StartUnix: proto.Int64(startUnix), DurationSec: proto.Int64(4),
			}
			if tc.absolute {
				request.Mode = sharedpb.ClipMode_CLIP_MODE_ABSOLUTE
				request.StopUnix = proto.Int64(stopUnix)
				request.DurationSec = nil
			}
			_, err := s.CreateClip(ctxAs("user-1", tenant, "owner"), request)
			if status.Code(err) != tc.wantErr {
				t.Fatalf("CreateClip returned %v, want status %s", err, tc.wantErr)
			}

			for cluster, reader := range readers {
				archive, shouldRead := expectedReads[cluster]
				if !shouldRead {
					if len(reader.requests) != 0 {
						t.Errorf("%s unexpectedly received %d archive reads", cluster, len(reader.requests))
					}
					continue
				}
				if len(reader.requests) != 1 {
					t.Errorf("%s received %d archive reads, want 1", cluster, len(reader.requests))
					continue
				}
				got := <-reader.requests
				if got.GetMode() != foghorncontrolpb.ArchivedClipCoverageMode ||
					got.GetTenantId() != tenant || got.GetDvrArtifactId() != archive.dvrHash ||
					got.GetRangeStartMs() != startMs || got.GetRangeEndMs() != stopMs {
					t.Errorf("%s received incorrect archive lookup: %v", cluster, got)
				}
			}
			for cluster, owner := range owners {
				if cluster != tc.wantCluster {
					if len(owner.requests) != 0 {
						t.Errorf("%s unexpectedly received %d clip mutations", cluster, len(owner.requests))
					}
					continue
				}
				if len(owner.requests) != 1 {
					t.Errorf("%s received %d clip mutations, want 1", cluster, len(owner.requests))
					continue
				}
				got := <-owner.requests
				if got.GetClusterId() != cluster || got.GetTenantId() != tenant ||
					got.GetStreamId() != stream || got.GetStreamInternalName() != "stream-internal" ||
					got.GetRequestId() != "request-1" || got.GetClipHash() == "" {
					t.Errorf("%s received incorrect durable clip identity: %v", cluster, got)
				}
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
