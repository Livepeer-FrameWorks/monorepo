package control

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// ListActiveClips joins artifacts→artifact_nodes for clip rows, excluding
// deleted ones; the row scans into the 8-field ClipRecord.
func TestListActiveClips(t *testing.T) {
	_, mock := setupRepoTest(t)
	repo := &clipRepositoryDB{}
	mock.ExpectQuery(`FROM foghorn.artifacts a\s+LEFT JOIN foghorn.artifact_nodes n.*WHERE a.artifact_type = 'clip' AND a.status != 'deleted'`).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "tenant", "internal", "node", "status", "path", "size", "loc"}).
			AddRow("clip-1", "", "live+x", "node-1", "ready", "/d/clip.mp4", int64(2048), "s3"))
	out, err := repo.ListActiveClips(context.Background())
	if err != nil || len(out) != 1 || out[0].ClipHash != "clip-1" || out[0].Status != "ready" {
		t.Fatalf("got (%+v,%v)", out, err)
	}
}

// ResolveInternalNameByRequestID returns the owning stream name, or "" (no error)
// when the request id maps to no clip row.
func TestResolveInternalNameByRequestID(t *testing.T) {
	t.Run("found", func(t *testing.T) {
		_, mock := setupRepoTest(t)
		repo := &clipRepositoryDB{}
		mock.ExpectQuery(`SELECT COALESCE\(stream_internal_name,''\) FROM foghorn.artifacts\s+WHERE request_id = \$1 AND artifact_type = 'clip'`).
			WithArgs("req-1").
			WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("live+x"))
		got, err := repo.ResolveInternalNameByRequestID(context.Background(), "req-1")
		if err != nil || got != "live+x" {
			t.Fatalf("got (%q,%v)", got, err)
		}
	})
	t.Run("no rows maps to empty,nil", func(t *testing.T) {
		_, mock := setupRepoTest(t)
		repo := &clipRepositoryDB{}
		mock.ExpectQuery(`WHERE request_id = \$1 AND artifact_type = 'clip'`).
			WithArgs("missing").
			WillReturnError(sql.ErrNoRows)
		got, err := repo.ResolveInternalNameByRequestID(context.Background(), "missing")
		if err != nil || got != "" {
			t.Fatalf("got (%q,%v), want (\"\",nil)", got, err)
		}
	})
}

// NeedsDtshSync (clip and vod variants) is an EXISTS probe: a synced artifact
// whose .dtsh wasn't included yet. Any query error reads false (fail-safe).
func TestNeedsDtshSyncProbes(t *testing.T) {
	t.Run("clip needs sync", func(t *testing.T) {
		_, mock := setupRepoTest(t)
		repo := &clipRepositoryDB{}
		mock.ExpectQuery(`SELECT EXISTS\(.*artifact_type = 'clip'.*sync_status = 'synced'.*dtsh_synced`).
			WithArgs("clip-1").
			WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		if !repo.NeedsDtshSync(context.Background(), "clip-1") {
			t.Fatal("expected true")
		}
	})
	t.Run("vod query error is false", func(t *testing.T) {
		_, mock := setupRepoTest(t)
		repo := &artifactRepositoryDB{}
		mock.ExpectQuery(`artifact_type = 'vod'`).
			WithArgs("vod-1").
			WillReturnError(errors.New("boom"))
		if repo.NeedsVODDtshSync(context.Background(), "vod-1") {
			t.Fatal("query error should read false")
		}
	})
}

// ListAllDVR scans dvr artifact rows into the 9-field DVRRecord.
func TestListAllDVR(t *testing.T) {
	_, mock := setupRepoTest(t)
	repo := &dvrRepositoryDB{}
	mock.ExpectQuery(`WHERE a.artifact_type = 'dvr'`).
		WillReturnRows(sqlmock.NewRows([]string{"hash", "tenant", "internal", "node", "base", "status", "dur", "size", "manifest"}).
			AddRow("dvr-1", "", "live+x", "node-1", "https://n1", "recording", int64(60), int64(9000), "/m.m3u8"))
	out, err := repo.ListAllDVR(context.Background())
	if err != nil || len(out) != 1 || out[0].Hash != "dvr-1" || out[0].Status != "recording" {
		t.Fatalf("got (%+v,%v)", out, err)
	}
}

// ResolveInternalNameByHash mirrors the clip variant for DVR artifacts.
func TestResolveInternalNameByHash(t *testing.T) {
	_, mock := setupRepoTest(t)
	repo := &dvrRepositoryDB{}
	mock.ExpectQuery(`WHERE artifact_hash = \$1 AND artifact_type = 'dvr'`).
		WithArgs("dvr-1").
		WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("live+x"))
	got, err := repo.ResolveInternalNameByHash(context.Background(), "dvr-1")
	if err != nil || got != "live+x" {
		t.Fatalf("got (%q,%v)", got, err)
	}
}

// UpdateDVRProgressByHash promotes a pre-terminal DVR and grows its size
// monotonically (GREATEST), gated on non-terminal statuses.
func TestUpdateDVRProgressByHash(t *testing.T) {
	_, mock := setupRepoTest(t)
	repo := &dvrRepositoryDB{}
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET status = \$2,\s+size_bytes = GREATEST.*WHERE artifact_hash = \$1\s+AND artifact_type = 'dvr'\s+AND status IN \('requested', 'starting', 'recording'\)`).
		WithArgs("dvr-1", "recording", int64(4096)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.UpdateDVRProgressByHash(context.Background(), "dvr-1", "recording", 4096); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// UpdateDVRCompletionByHash writes terminal fields only while still pre-terminal,
// so a racing FinalizeDVR that landed first wins.
func TestUpdateDVRCompletionByHash(t *testing.T) {
	_, mock := setupRepoTest(t)
	repo := &dvrRepositoryDB{}
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET status = \$1,\s+ended_at = NOW\(\).*WHERE artifact_hash = \$6\s+AND artifact_type = 'dvr'\s+AND status IN \('requested', 'starting', 'recording', 'finalizing'\)`).
		WithArgs("completed", int64(120), int64(9000), "/m.m3u8", "", "dvr-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.UpdateDVRCompletionByHash(context.Background(), "dvr-1", "completed", 120, 9000, "/m.m3u8", ""); err != nil {
		t.Fatal(err)
	}
}

// ListAllNodes returns node_outputs rows (node_id, base_url, outputs JSON).
func TestListAllNodes(t *testing.T) {
	_, mock := setupRepoTest(t)
	repo := &nodeRepositoryDB{}
	mock.ExpectQuery(`SELECT node_id, COALESCE\(base_url,''\), COALESCE\(outputs,'\{\}'\) FROM foghorn.node_outputs`).
		WillReturnRows(sqlmock.NewRows([]string{"node_id", "base_url", "outputs"}).
			AddRow("node-1", "https://n1", `{"k":"v"}`))
	out, err := repo.ListAllNodes(context.Background())
	if err != nil || len(out) != 1 || out[0].NodeID != "node-1" || out[0].OutputsJSON != `{"k":"v"}` {
		t.Fatalf("got (%+v,%v)", out, err)
	}
}

// GetArtifactSyncInfo reads the artifact's sync row then its cached nodes; a
// missing artifact maps to (nil, nil).
func TestGetArtifactSyncInfo(t *testing.T) {
	t.Run("found with cached node", func(t *testing.T) {
		_, mock := setupRepoTest(t)
		repo := &artifactRepositoryDB{}
		mock.ExpectQuery(`SELECT artifact_hash, artifact_type, COALESCE\(sync_status,'pending'\).*FROM foghorn.artifacts\s+WHERE artifact_hash = \$1`).
			WithArgs("art-1").
			WillReturnRows(sqlmock.NewRows([]string{"hash", "type", "sync_status", "s3_url", "last_attempt", "sync_error"}).
				AddRow("art-1", "clip", "synced", "s3://b/art-1", time.Unix(1700000000, 0), nil))
		mock.ExpectQuery(`SELECT node_id, cached_at FROM foghorn.artifact_nodes\s+WHERE artifact_hash = \$1 AND is_orphaned = false`).
			WithArgs("art-1").
			WillReturnRows(sqlmock.NewRows([]string{"node_id", "cached_at"}).
				AddRow("node-1", time.Unix(1700000000, 0)))
		info, err := repo.GetArtifactSyncInfo(context.Background(), "art-1")
		if err != nil || info == nil || info.SyncStatus != "synced" || info.S3URL != "s3://b/art-1" {
			t.Fatalf("got (%+v,%v)", info, err)
		}
		if len(info.CachedNodes) != 1 || info.CachedNodes[0] != "node-1" {
			t.Fatalf("cached nodes = %v", info.CachedNodes)
		}
	})
	t.Run("missing artifact maps to nil,nil", func(t *testing.T) {
		_, mock := setupRepoTest(t)
		repo := &artifactRepositoryDB{}
		mock.ExpectQuery(`FROM foghorn.artifacts\s+WHERE artifact_hash = \$1`).
			WithArgs("missing").
			WillReturnError(sql.ErrNoRows)
		info, err := repo.GetArtifactSyncInfo(context.Background(), "missing")
		if err != nil || info != nil {
			t.Fatalf("got (%+v,%v), want (nil,nil)", info, err)
		}
	})
}

// RegisterOriginArtifact upserts an origin (canonical full-file) node row.
func TestRegisterOriginArtifact(t *testing.T) {
	_, mock := setupRepoTest(t)
	repo := &artifactRepositoryDB{}
	mock.ExpectExec(`INSERT INTO foghorn.artifact_nodes.*'origin'.*ON CONFLICT \(artifact_hash, node_id\) DO UPDATE`).
		WithArgs("art-1", "node-1", "/d/art-1.mp4", int64(4096), true).
		WillReturnResult(sqlmock.NewResult(0, 1))
	if err := repo.RegisterOriginArtifact(context.Background(), "art-1", "node-1", "/d/art-1.mp4", 4096, true); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// ListOriginNodes returns complete, non-orphaned origin holders for peer relay.
func TestListOriginNodes(t *testing.T) {
	_, mock := setupRepoTest(t)
	repo := &artifactRepositoryDB{}
	mock.ExpectQuery(`SELECT node_id FROM foghorn.artifact_nodes\s+WHERE artifact_hash = \$1\s+AND role = 'origin'\s+AND is_complete = true\s+AND is_orphaned = false`).
		WithArgs("art-1").
		WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("node-1").AddRow("node-2"))
	out, err := repo.ListOriginNodes(context.Background(), "art-1")
	if err != nil || len(out) != 2 || out[0] != "node-1" {
		t.Fatalf("got (%v,%v)", out, err)
	}
}

// GetCachedAt returns the earliest cached_at in ms, or 0 when NULL (no cached
// node yet).
func TestGetCachedAt(t *testing.T) {
	t.Run("valid timestamp", func(t *testing.T) {
		_, mock := setupRepoTest(t)
		repo := &artifactRepositoryDB{}
		mock.ExpectQuery(`SELECT MIN\(cached_at\) FROM foghorn.artifact_nodes\s+WHERE artifact_hash = \$1 AND is_orphaned = false`).
			WithArgs("art-1").
			WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(time.Unix(1700000000, 0)))
		got, err := repo.GetCachedAt(context.Background(), "art-1")
		if err != nil || got != time.Unix(1700000000, 0).UnixMilli() {
			t.Fatalf("got (%d,%v)", got, err)
		}
	})
	t.Run("null maps to zero", func(t *testing.T) {
		_, mock := setupRepoTest(t)
		repo := &artifactRepositoryDB{}
		mock.ExpectQuery(`SELECT MIN\(cached_at\)`).
			WithArgs("art-1").
			WillReturnRows(sqlmock.NewRows([]string{"min"}).AddRow(nil))
		got, err := repo.GetCachedAt(context.Background(), "art-1")
		if err != nil || got != 0 {
			t.Fatalf("got (%d,%v), want (0,nil)", got, err)
		}
	})
}

// ListAllNodeArtifacts groups artifact records by node for the warm-cache view.
func TestListAllNodeArtifacts(t *testing.T) {
	_, mock := setupRepoTest(t)
	repo := &artifactRepositoryDB{}
	mock.ExpectQuery(`FROM foghorn.artifact_nodes an\s+JOIN foghorn.artifacts a.*WHERE an.is_orphaned = false\s+AND a.status != 'deleted'`).
		WillReturnRows(sqlmock.NewRows([]string{"node", "hash", "type", "internal", "path", "size", "created", "access", "last"}).
			AddRow("node-1", "art-1", "clip", "live+x", "/d/a", int64(2048), int64(1700000000), int64(3), int64(1700000100)).
			AddRow("node-1", "art-2", "dvr", "live+y", "/d/b", int64(4096), int64(1700000200), int64(1), int64(1700000300)))
	out, err := repo.ListAllNodeArtifacts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(out["node-1"]) != 2 || out["node-1"][0].ArtifactHash != "art-1" {
		t.Fatalf("got %+v", out)
	}
}

// Repo methods fail closed with ErrConnDone (or false for bool probes) on a nil
// DB handle.
func TestReposMore_NilDBGuards(t *testing.T) {
	prev := db
	db = nil
	t.Cleanup(func() { db = prev })
	ctx := context.Background()

	if _, err := (&clipRepositoryDB{}).ListActiveClips(ctx); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("ListActiveClips nil db = %v", err)
	}
	if _, err := (&dvrRepositoryDB{}).ListAllDVR(ctx); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("ListAllDVR nil db = %v", err)
	}
	if err := (&dvrRepositoryDB{}).UpdateDVRProgressByHash(ctx, "h", "recording", 1); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("UpdateDVRProgressByHash nil db = %v", err)
	}
	if _, err := (&nodeRepositoryDB{}).ListAllNodes(ctx); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("ListAllNodes nil db = %v", err)
	}
	if _, err := (&artifactRepositoryDB{}).GetArtifactSyncInfo(ctx, "h"); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("GetArtifactSyncInfo nil db = %v", err)
	}
	if _, err := (&artifactRepositoryDB{}).ListOriginNodes(ctx, "h"); !errors.Is(err, sql.ErrConnDone) {
		t.Errorf("ListOriginNodes nil db = %v", err)
	}
	if (&artifactRepositoryDB{}).NeedsVODDtshSync(ctx, "h") {
		t.Error("NeedsVODDtshSync nil db should be false")
	}
}
