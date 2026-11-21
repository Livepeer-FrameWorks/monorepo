package grpc

import (
	"context"
	"fmt"
	"net"
	"slices"
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
	"google.golang.org/protobuf/types/known/timestamppb"
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
	chapters map[string]*foghorncontrolpb.ChapterRef
	legacy   bool
	delay    time.Duration
}

func (r *clipOriginChapterReader) ListDVRChapters(ctx context.Context, req *foghorncontrolpb.ListDVRChaptersRequest) (*foghorncontrolpb.ListDVRChaptersResponse, error) {
	r.requests <- req
	if r.delay > 0 {
		timer := time.NewTimer(r.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, status.FromContextError(ctx.Err()).Err()
		case <-timer.C:
		}
	}
	response := &foghorncontrolpb.ListDVRChaptersResponse{}
	if r.legacy {
		return response, nil
	}
	grpc.SetTrailer(ctx, metadata.Pairs(foghorncontrolpb.ArchivedClipCoverageTrailer, foghorncontrolpb.ArchivedClipCoverageVersion))
	for _, dvrID := range req.GetArchiveDvrArtifactIds() {
		if chapter := r.chapters[dvrID]; chapter != nil {
			response.Chapters = append(response.Chapters, chapter)
		}
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
	retiredArchive := differentArchive
	retiredArchive.cluster = "retired-archive"
	otherParent := clipOriginArchive{
		dvrHash: "archive-dvr-other", cluster: reclaimedArchive.cluster, catalogHash: "different-chapter",
	}
	wrongParentArchive := reclaimedArchive
	wrongParentArchive.chapter = proto.CloneOf(reclaimedArchive.chapter)
	wrongParentArchive.chapter.ArchiveDvrArtifactId = otherParent.dvrHash
	history := make([]clipOriginArchive, 60)
	history[0] = reclaimedArchive
	history[0].dvrHash = "archive-dvr-00"
	for i := 1; i < len(history); i++ {
		history[i] = clipOriginArchive{
			dvrHash: fmt.Sprintf("archive-dvr-%02d", i), cluster: reclaimedArchive.cluster,
			catalogHash: fmt.Sprintf("old-chapter-%02d", i),
		}
	}
	for _, tc := range []struct {
		name, wantCluster string
		activeAt          any
		archives          []clipOriginArchive
		absolute          bool
		legacyCluster     string
		offlineCluster    string
		expiredCluster    string
		readDelay         time.Duration
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
		{
			name: "stale ingest skips de-entitled archive origin", wantCluster: "archive-a",
			activeAt: time.Now().Add(-time.Hour), archives: []clipOriginArchive{retiredArchive, reclaimedArchive},
		},
		{
			name: "stale ingest skips an expired cached grant", wantCluster: "archive-a",
			activeAt: time.Now().Add(-time.Hour), archives: []clipOriginArchive{reclaimedArchive, differentArchive}, expiredCluster: differentArchive.cluster,
		},
		{
			name:     "expired cached default cannot receive a fallback create",
			activeAt: time.Now().Add(-time.Hour), archives: []clipOriginArchive{partialArchive}, expiredCluster: "platform-cell", wantErr: codes.PermissionDenied,
		},
		{
			name: "stale ingest batches sixty DVRs on one origin", wantCluster: "archive-a",
			activeAt: time.Now().Add(-time.Hour), archives: history, readDelay: 100 * time.Millisecond,
		},
		{
			name: "eligible archive without endpoint fails closed", activeAt: time.Now().Add(-time.Hour),
			archives: []clipOriginArchive{reclaimedArchive, differentArchive}, offlineCluster: differentArchive.cluster, wantErr: codes.Unavailable,
		},
		{
			name: "returned chapter must match its registered parent", wantCluster: "platform-cell",
			activeAt: time.Now().Add(-time.Hour), archives: []clipOriginArchive{wrongParentArchive, otherParent},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, mock, closeDB := newMockServer(t)
			t.Cleanup(closeDB)
			owners := make(map[string]*clipOriginOwner)
			readers := make(map[string]*clipOriginChapterReader)
			expectedReads := make(map[string][]clipOriginArchive)
			for _, archive := range tc.archives {
				if archive.cluster != retiredArchive.cluster && archive.cluster != tc.offlineCluster && archive.cluster != tc.expiredCluster {
					expectedReads[archive.cluster] = append(expectedReads[archive.cluster], archive)
				}
			}
			route := &clusterRoute{clusterID: "platform-cell", resolvedAt: time.Now(), admissionResolvedAt: time.Now()}
			for _, cluster := range []string{"platform-cell", "private-edge", "archive-a", "archive-b", "untrusted-caller-cluster", retiredArchive.cluster} {
				listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				owner := &clipOriginOwner{requests: make(chan *sharedpb.CreateClipRequest, 8)}
				reader := &clipOriginChapterReader{
					requests: make(chan *foghorncontrolpb.ListDVRChaptersRequest, max(8, len(tc.archives))),
					chapters: make(map[string]*foghorncontrolpb.ChapterRef),
					legacy:   tc.legacyCluster == cluster,
					delay:    tc.readDelay,
				}
				for _, archive := range expectedReads[cluster] {
					if archive.chapter == nil {
						continue
					}
					chapter := proto.CloneOf(archive.chapter)
					if chapter.GetArchiveDvrArtifactId() == "" {
						chapter.ArchiveDvrArtifactId = archive.dvrHash
					}
					reader.chapters[archive.dvrHash] = chapter
				}
				owners[cluster], readers[cluster] = owner, reader
				rpc := grpc.NewServer()
				foghornpb.RegisterClipControlServiceServer(rpc, owner)
				foghornpb.RegisterDVRControlServiceServer(rpc, reader)
				go func() { _ = rpc.Serve(listener) }()
				t.Cleanup(func() { rpc.Stop(); _ = listener.Close() })
				if cluster == retiredArchive.cluster {
					continue
				}
				peer := &clusterpeerpb.TenantClusterPeer{
					ClusterId: cluster, FoghornGrpcAddr: listener.Addr().String(), HealthStatus: "healthy",
					AccessSource: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER, AccessActive: true,
				}
				if cluster == tc.expiredCluster {
					peer.AccessExpiresAt = timestamppb.New(time.Now().Add(-time.Second))
				}
				if cluster == tc.offlineCluster {
					peer.FoghornGrpcAddr = ""
					peer.HealthStatus = "offline"
					route.admissionPeers = append(route.admissionPeers, peer)
					continue
				}
				route.admissionPeers = append(route.admissionPeers, peer)
				if cluster == route.clusterID {
					route.foghornAddr = listener.Addr().String()
				} else {
					route.clusterPeers = append(route.clusterPeers, peer)
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
				archives, shouldRead := expectedReads[cluster]
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
				wantIDs := make([]string, 0, len(archives))
				for _, archive := range archives {
					wantIDs = append(wantIDs, archive.dvrHash)
				}
				slices.Sort(wantIDs)
				if got.GetMode() != foghorncontrolpb.ArchivedClipCoverageMode ||
					got.GetTenantId() != tenant || got.GetDvrArtifactId() != "" ||
					!slices.Equal(got.GetArchiveDvrArtifactIds(), wantIDs) ||
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
