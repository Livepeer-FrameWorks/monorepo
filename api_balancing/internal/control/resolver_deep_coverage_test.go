package control

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// INVARIANT: resolveChapterArtifactContent only resolves a chapter VOD when the
// input is a Commodore-minted chapter playback_id resolving to a dvr_chapter
// origin artifact in a playable state, and it stamps the result vod+<hash> with
// auth inherited from the parent DVR. Raw artifact hashes are never accepted.
func TestResolveChapterArtifactContent(t *testing.T) {
	t.Run("unknown chapter playback id falls through (nil)", func(t *testing.T) {
		mock := setupChapterTest(t)
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			chapterPlaybackID: func(_ context.Context, _ *commodorepb.ResolveChapterPlaybackIDRequest) (*commodorepb.ResolveChapterPlaybackIDResponse, error) {
				return &commodorepb.ResolveChapterPlaybackIDResponse{Found: false}, nil
			},
		})
		if got := resolveChapterArtifactContent(context.Background(), "not-a-chapter"); got != nil {
			t.Fatalf("non-chapter input must fall through, got %+v", got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("must not touch the artifacts table: %v", err)
		}
	})

	t.Run("non-playable chapter state refuses playback (nil)", func(t *testing.T) {
		mock := setupChapterTest(t)
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			chapterPlaybackID: chapterFound(chapterHash32),
		})
		mock.ExpectQuery(`SELECT origin_type, origin_id, tenant_id::text,`).
			WithArgs(chapterHash32).
			WillReturnRows(chapterArtifactContentRow("dvr_chapter", "chap-pending", "t1"))
		// Chapter is still 'finalizing' — the .mkv doesn't exist yet.
		mock.ExpectQuery(`FROM foghorn.dvr_chapters\s+WHERE chapter_id = \$1`).
			WithArgs("chap-pending").
			WillReturnRows(playableChapterRow("chap-pending", "parent-dvr", ChapterStateFinalizing))

		if got := resolveChapterArtifactContent(context.Background(), "pb-chap"); got != nil {
			t.Fatalf("a not-yet-playable chapter must not resolve, got %+v", got)
		}
	})

	t.Run("playable chapter resolves vod+ with parent auth", func(t *testing.T) {
		mock := setupChapterTest(t)
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			chapterPlaybackID: chapterFound(chapterHash32),
			dvrHash: func(_ context.Context, _ *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error) {
				return &commodorepb.ResolveDVRHashResponse{
					Found: true, TenantId: "parent-tenant", StreamId: "parent-stream", PlaybackId: "parent-pb",
				}, nil
			},
			// Parent playback policy says public (RequiresAuth=false).
			artifactPlaybackID: func(_ context.Context, req *commodorepb.ResolveArtifactPlaybackIDRequest) (*commodorepb.ResolveArtifactPlaybackIDResponse, error) {
				if req.GetPlaybackId() != "parent-pb" {
					t.Errorf("policy lookup must use parent playback id, got %q", req.GetPlaybackId())
				}
				return &commodorepb.ResolveArtifactPlaybackIDResponse{Found: true, RequiresAuth: false}, nil
			},
		})
		mock.ExpectQuery(`SELECT origin_type, origin_id, tenant_id::text,`).
			WithArgs(chapterHash32).
			WillReturnRows(chapterArtifactContentRow("dvr_chapter", "chap-ok", "t1"))
		mock.ExpectQuery(`FROM foghorn.dvr_chapters\s+WHERE chapter_id = \$1`).
			WithArgs("chap-ok").
			WillReturnRows(playableChapterRow("chap-ok", "parent-dvr", ChapterStateFinalized))

		got := resolveChapterArtifactContent(context.Background(), "pb-chap")
		if got == nil {
			t.Fatal("playable chapter must resolve")
		}
		// DECISION: content is a vod+<artifact_hash> from the parent DVR, with
		// parent-stream/tenant and policy-derived auth.
		if got.ContentType != "vod" {
			t.Fatalf("chapter resolves as vod, got %q", got.ContentType)
		}
		if got.InternalName != "vod+"+chapterHash32 {
			t.Fatalf("internal name must be vod+<hash>, got %q", got.InternalName)
		}
		if got.ContentId != "pb-chap" {
			t.Fatalf("content id must stay the public playback id, got %q", got.ContentId)
		}
		if got.TenantId != "parent-tenant" || got.StreamId != "parent-stream" {
			t.Fatalf("tenant/stream must come from parent DVR, got %+v", got)
		}
		if got.RequiresAuth {
			t.Fatal("parent policy said public; RequiresAuth must be false")
		}
	})

	t.Run("parent policy unreachable fails closed (RequiresAuth stays true)", func(t *testing.T) {
		mock := setupChapterTest(t)
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			chapterPlaybackID: chapterFound(chapterHash32),
			dvrHash: func(_ context.Context, _ *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error) {
				// No parent playback id → policy lookup is skipped entirely.
				return &commodorepb.ResolveDVRHashResponse{Found: true, TenantId: "pt", StreamId: "ps"}, nil
			},
		})
		mock.ExpectQuery(`SELECT origin_type, origin_id, tenant_id::text,`).
			WithArgs(chapterHash32).
			WillReturnRows(chapterArtifactContentRow("dvr_chapter", "chap-fc", "t1"))
		mock.ExpectQuery(`FROM foghorn.dvr_chapters\s+WHERE chapter_id = \$1`).
			WithArgs("chap-fc").
			WillReturnRows(playableChapterRow("chap-fc", "parent-dvr", ChapterStateFinalized))

		got := resolveChapterArtifactContent(context.Background(), "pb-chap")
		if got == nil {
			t.Fatal("expected resolution")
		}
		if !got.RequiresAuth {
			t.Fatal("with no parent policy confirmation, chapter must fail closed (RequiresAuth=true)")
		}
	})
}

// INVARIANT: ResolveArtifactPlayback routes a chapter playback id through the
// chapter-synth path (resolveChapterArtifactPlaybackResp) BEFORE the generic VOD
// registry, so the chapter inherits parent-DVR tenant/auth even though Commodore
// also keeps a hidden VOD registry row. The full resolve then lands on the warm
// node holding the chapter MKV.
func TestResolveArtifactPlayback_ChapterPath(t *testing.T) {
	ctx := context.Background()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	lat, lon := 52.0, 5.0
	sm.SetNodeInfo("cn1", "https://cn1.example.com", true, &lat, &lon, "ams", "", map[string]any{"HLS": "x"})
	sm.TouchNode("cn1", true)
	sm.SetNodeArtifacts("cn1", []*ipcpb.StoredArtifact{{ClipHash: chapterHash32}})

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mockDB.Close() })
	prevDB := db
	db = mockDB
	t.Cleanup(func() { db = prevDB })

	startFakeCommodoreServer(t, &fakeCommodoreInternal{
		chapterPlaybackID: chapterFound(chapterHash32),
		dvrHash: func(_ context.Context, _ *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error) {
			return &commodorepb.ResolveDVRHashResponse{Found: true, TenantId: "t-chap", StreamId: "s-chap"}, nil
		},
		// vod arm in resolveArtifactPlaybackWithResp re-resolves the vod hash.
		vodHash: func(_ context.Context, _ *commodorepb.ResolveVodHashRequest) (*commodorepb.ResolveVodHashResponse, error) {
			return &commodorepb.ResolveVodHashResponse{Found: false}, nil
		},
	})

	// resolveChapterArtifactPlaybackResp: artifacts row lookup (4 cols).
	mock.ExpectQuery(`SELECT origin_type, origin_id, tenant_id::text,\s+COALESCE\(internal_name`).
		WithArgs(chapterHash32).
		WillReturnRows(chapterArtifactPlaybackRow("dvr_chapter", "chap-play", "t-chap"))
	// GetChapter for chap-play.
	mock.ExpectQuery(`FROM foghorn.dvr_chapters\s+WHERE chapter_id = \$1`).
		WithArgs("chap-play").
		WillReturnRows(playableChapterRow("chap-play", "parent-dvr", ChapterStateFinalized))
	// resolveArtifactPlaybackWithResp: foghorn.artifacts placement lookup. Tenant
	// = parent DVR tenant (t-chap). Empty authoritative cluster = always serveable.
	mock.ExpectQuery(`FROM foghorn.artifacts\s+WHERE artifact_hash = \$1 AND artifact_type = \$2`).
		WithArgs(chapterHash32, "vod", "t-chap").
		WillReturnRows(sqlmock.NewRows([]string{
			"internal_name", "status", "duration_seconds", "size_bytes", "created_at",
			"format", "storage_location", "sync_status", "has_thumbnails", "authoritative_cluster",
		}).AddRow("vod+"+chapterHash32, "ready", int64(30), int64(1234), nil, "mkv", "local", "pending", false, ""))

	resp, err := ResolveArtifactPlayback(ctx, &PlaybackDependencies{DB: mockDB, LocalClusterID: "c1", GeoLat: 52, GeoLon: 5}, "pb-chap")
	if err != nil {
		t.Fatalf("chapter playback resolution failed: %v", err)
	}
	if resp.GetPrimary() == nil || resp.GetPrimary().GetNodeId() != "cn1" {
		t.Fatalf("expected warm chapter node cn1, got %+v", resp.GetPrimary())
	}
	// Tenant on the metadata is the parent DVR's tenant, proving the chapter-synth
	// path (not the generic registry) drove the resolution.
	if md := resp.GetMetadata(); md == nil || md.GetTenantId() != "t-chap" || md.GetContentType() != "vod" {
		t.Fatalf("expected parent-DVR tenant vod metadata, got %+v", resp.GetMetadata())
	}
}

// INVARIANT: the clip content arm of resolveArtifactPlaybackWithResp enriches
// metadata from Commodore's clip registry — title and clip-source (internal
// name) — and surfaces the clip's own playback id. Pins the clip routing arm.
func TestResolveArtifactPlayback_ClipArm(t *testing.T) {
	ctx := context.Background()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	lat, lon := 52.0, 5.0
	sm.SetNodeInfo("clipn", "https://clipn.example.com", true, &lat, &lon, "ams", "", map[string]any{"HLS": "x"})
	sm.TouchNode("clipn", true)
	sm.SetNodeArtifacts("clipn", []*ipcpb.StoredArtifact{{ClipHash: "cliphash1"}})

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mockDB.Close() })

	startFakeCommodoreServer(t, &fakeCommodoreInternal{
		artifactPlaybackID: foundArtifact("cliphash1", "clip", "tclip", ""),
		clipHash: func(_ context.Context, _ *commodorepb.ResolveClipHashRequest) (*commodorepb.ResolveClipHashResponse, error) {
			return &commodorepb.ResolveClipHashResponse{
				Found: true, TenantId: "tclip", StreamId: "src-stream",
				InternalName: "live+source", Title: "My Clip", Description: "desc",
				Duration: 8000, PlaybackId: "clip-pb",
			}, nil
		},
	})

	mock.ExpectQuery(`FROM foghorn.artifacts\s+WHERE artifact_hash = \$1 AND artifact_type = \$2`).
		WithArgs("cliphash1", "clip", "tclip").
		WillReturnRows(sqlmock.NewRows([]string{
			"internal_name", "status", "duration_seconds", "size_bytes", "created_at",
			"format", "storage_location", "sync_status", "has_thumbnails", "authoritative_cluster",
		}).AddRow("live+source", "ready", int64(8), int64(2048), nil, "mp4", "local", "synced", false, ""))

	resp, err := ResolveArtifactPlayback(ctx, &PlaybackDependencies{DB: mockDB, LocalClusterID: "c1", GeoLat: 52, GeoLon: 5}, "clip-pb")
	if err != nil {
		t.Fatalf("clip resolution failed: %v", err)
	}
	md := resp.GetMetadata()
	if md == nil || md.GetContentType() != "clip" {
		t.Fatalf("expected clip metadata, got %+v", md)
	}
	if md.GetTitle() != "My Clip" {
		t.Fatalf("clip title not enriched from registry: %q", md.GetTitle())
	}
	if md.GetClipSource() != "live+source" {
		t.Fatalf("clip source (internal name) not surfaced: %q", md.GetClipSource())
	}
	// Clip duration comes from the registry (ms→s), not the artifact row.
	if md.GetDurationSeconds() != 8 {
		t.Fatalf("clip duration should be 8s from registry, got %d", md.GetDurationSeconds())
	}
}

// INVARIANT: the dvr content arm marks IsLive only while the recording is still
// in progress (status='recording'); a finished DVR is VOD-like (IsLive=false)
// and carries DvrStatus. Pins the live-vs-finished DVR distinction.
func TestResolveArtifactPlayback_DvrArmIsLive(t *testing.T) {
	ctx := context.Background()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	lat, lon := 52.0, 5.0
	sm.SetNodeInfo("dvrn", "https://dvrn.example.com", true, &lat, &lon, "ams", "", map[string]any{"HLS": "x"})
	sm.TouchNode("dvrn", true)
	sm.SetNodeArtifacts("dvrn", []*ipcpb.StoredArtifact{{ClipHash: "dvrhash1"}})

	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mockDB.Close() })

	startFakeCommodoreServer(t, &fakeCommodoreInternal{
		artifactPlaybackID: foundArtifact("dvrhash1", "dvr", "tdvr", ""),
		dvrHash: func(_ context.Context, _ *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error) {
			return &commodorepb.ResolveDVRHashResponse{
				Found: true, TenantId: "tdvr", StreamId: "dvr-stream", PlaybackId: "dvr-pb",
			}, nil
		},
	})

	mock.ExpectQuery(`FROM foghorn.artifacts\s+WHERE artifact_hash = \$1 AND artifact_type = \$2`).
		WithArgs("dvrhash1", "dvr", "tdvr").
		WillReturnRows(sqlmock.NewRows([]string{
			"internal_name", "status", "duration_seconds", "size_bytes", "created_at",
			"format", "storage_location", "sync_status", "has_thumbnails", "authoritative_cluster",
		}).AddRow("dvr+x", "recording", int64(0), int64(0), nil, "mp4", "local", "synced", false, ""))

	resp, err := ResolveArtifactPlayback(ctx, &PlaybackDependencies{DB: mockDB, LocalClusterID: "c1", GeoLat: 52, GeoLon: 5}, "dvr-pb")
	if err != nil {
		t.Fatalf("dvr resolution failed: %v", err)
	}
	md := resp.GetMetadata()
	if md == nil || md.GetContentType() != "dvr" {
		t.Fatalf("expected dvr metadata, got %+v", md)
	}
	// DECISION: a still-recording DVR is live.
	if !md.GetIsLive() {
		t.Fatal("a DVR with status='recording' must be IsLive=true")
	}
	if md.GetDvrStatus() != "recording" {
		t.Fatalf("dvr status must be surfaced, got %q", md.GetDvrStatus())
	}
}

// chapterFound returns a ResolveChapterPlaybackID fake that resolves any input to
// the given artifact hash (a found chapter).
func chapterFound(artifactHash string) func(context.Context, *commodorepb.ResolveChapterPlaybackIDRequest) (*commodorepb.ResolveChapterPlaybackIDResponse, error) {
	return func(_ context.Context, _ *commodorepb.ResolveChapterPlaybackIDRequest) (*commodorepb.ResolveChapterPlaybackIDResponse, error) {
		return &commodorepb.ResolveChapterPlaybackIDResponse{Found: true, ArtifactHash: artifactHash}, nil
	}
}
