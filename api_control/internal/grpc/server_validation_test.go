package grpc

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/sirupsen/logrus"
)

func TestValidateStreamKey(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		req       *commodorepb.ValidateStreamKeyRequest
		setupMock func(sqlmock.Sqlmock)
		assert    func(*testing.T, *commodorepb.ValidateStreamKeyResponse, error)
	}{
		{
			name: "empty_stream_key",
			req:  &commodorepb.ValidateStreamKeyRequest{StreamKey: ""},
			assert: func(t *testing.T, resp *commodorepb.ValidateStreamKeyResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resp.Valid {
					t.Fatalf("expected invalid response")
				}
				if resp.Error != "stream_key required" {
					t.Fatalf("unexpected error message: %q", resp.Error)
				}
			},
		},
		{
			name: "invalid_stream_key",
			req:  &commodorepb.ValidateStreamKeyRequest{StreamKey: "bad-key"},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("FROM commodore.streams").WithArgs("bad-key").WillReturnError(sql.ErrNoRows)
			},
			assert: func(t *testing.T, resp *commodorepb.ValidateStreamKeyResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resp.Valid {
					t.Fatalf("expected invalid response")
				}
				if resp.Error != "Invalid stream key" {
					t.Fatalf("unexpected error message: %q", resp.Error)
				}
			},
		},
		{
			name: "inactive_user",
			req:  &commodorepb.ValidateStreamKeyRequest{StreamKey: "inactive-key"},
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "internal_name", "is_active", "is_recording_enabled", "playback_id", "ingest_mode"}).
					AddRow("stream-id", "user-id", "tenant-id", "internal", false, true, "", "push")
				mock.ExpectQuery("FROM commodore.streams").WithArgs("inactive-key").WillReturnRows(rows)
			},
			assert: func(t *testing.T, resp *commodorepb.ValidateStreamKeyResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resp.Valid {
					t.Fatalf("expected invalid response")
				}
				if resp.Error != "User account is inactive" {
					t.Fatalf("unexpected error message: %q", resp.Error)
				}
			},
		},
		{
			name: "pull_stream_rejects_push_ingest",
			req:  &commodorepb.ValidateStreamKeyRequest{StreamKey: "pull-key"},
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "internal_name", "is_active", "is_recording_enabled", "playback_id", "ingest_mode"}).
					AddRow("stream-id", "user-id", "tenant-id", "internal", true, true, "pk_test123", "pull")
				mock.ExpectQuery("FROM commodore.streams").WithArgs("pull-key").WillReturnRows(rows)
			},
			assert: func(t *testing.T, resp *commodorepb.ValidateStreamKeyResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resp.Valid {
					t.Fatalf("expected invalid response")
				}
				if resp.Error != "Pull streams do not accept push ingest" {
					t.Fatalf("unexpected error message: %q", resp.Error)
				}
			},
		},
		{
			name: "active_user",
			req:  &commodorepb.ValidateStreamKeyRequest{StreamKey: "good-key", ClusterId: "cluster-us"},
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "internal_name", "is_active", "is_recording_enabled", "playback_id", "ingest_mode"}).
					AddRow("stream-id", "user-id", "tenant-id", "internal", true, true, "pk_test123", "push")
				mock.ExpectQuery("FROM commodore.streams").WithArgs("good-key").WillReturnRows(rows)
				mock.ExpectExec("UPDATE commodore.streams").WithArgs("cluster-us", "good-key").WillReturnResult(sqlmock.NewResult(0, 1))
			},
			assert: func(t *testing.T, resp *commodorepb.ValidateStreamKeyResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !resp.Valid {
					t.Fatalf("expected valid response")
				}
				if resp.BillingModel != "postpaid" {
					t.Fatalf("unexpected billing model: %q", resp.BillingModel)
				}
				if resp.InternalName != "internal" {
					t.Fatalf("unexpected internal name: %q", resp.InternalName)
				}
			},
		},
		{
			name: "active_user_cluster_update_contended",
			req:  &commodorepb.ValidateStreamKeyRequest{StreamKey: "contended-key", ClusterId: "cluster-eu"},
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "internal_name", "is_active", "is_recording_enabled", "playback_id", "ingest_mode"}).
					AddRow("stream-id", "user-id", "tenant-id", "internal", true, true, "pk_test123", "push")
				mock.ExpectQuery("FROM commodore.streams").WithArgs("contended-key").WillReturnRows(rows)
				mock.ExpectExec("UPDATE commodore.streams").WithArgs("cluster-eu", "contended-key").WillReturnResult(sqlmock.NewResult(0, 0))
			},
			assert: func(t *testing.T, resp *commodorepb.ValidateStreamKeyResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !resp.Valid {
					t.Fatalf("expected valid response")
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var db *sql.DB
			var mock sqlmock.Sqlmock
			if test.setupMock != nil {
				var err error
				db, mock, err = sqlmock.New()
				if err != nil {
					t.Fatalf("failed to create sqlmock: %v", err)
				}
				defer db.Close()
				test.setupMock(mock)
			}

			server := &CommodoreServer{db: db, logger: logrus.New()}
			resp, err := server.ValidateStreamKey(ctx, test.req)
			test.assert(t, resp, err)

			if test.setupMock != nil {
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unmet expectations: %v", err)
				}
			}
		})
	}
}

func TestResolveStreamContext(t *testing.T) {
	ctx := context.Background()
	cols := []string{"id", "user_id", "tenant_id", "internal_name", "is_active", "is_recording_enabled", "playback_id", "ingest_mode"}
	tests := []struct {
		name      string
		req       *commodorepb.ResolveStreamContextRequest
		setupMock func(sqlmock.Sqlmock)
		wantErr   bool
		assert    func(*testing.T, *commodorepb.ResolveStreamContextResponse)
	}{
		{
			name:    "no_identifier",
			req:     &commodorepb.ResolveStreamContextRequest{},
			wantErr: true,
		},
		{
			name:    "empty_stream_id",
			req:     &commodorepb.ResolveStreamContextRequest{Identifier: &commodorepb.ResolveStreamContextRequest_StreamId{StreamId: ""}},
			wantErr: true,
		},
		{
			name: "stream_not_found",
			req:  &commodorepb.ResolveStreamContextRequest{Identifier: &commodorepb.ResolveStreamContextRequest_StreamId{StreamId: "missing"}},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(`WHERE s\.id = \$1`).WithArgs("missing").WillReturnError(sql.ErrNoRows)
			},
			assert: func(t *testing.T, resp *commodorepb.ResolveStreamContextResponse) {
				if resp.Admitted {
					t.Fatalf("expected admitted=false for missing stream")
				}
				if resp.RejectionReason != commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_INVALID_KEY {
					t.Fatalf("unexpected rejection reason: %v", resp.RejectionReason)
				}
			},
		},
		{
			name: "inactive_user",
			req:  &commodorepb.ResolveStreamContextRequest{Identifier: &commodorepb.ResolveStreamContextRequest_PlaybackId{PlaybackId: "pk_inactive"}},
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows(cols).
					AddRow("stream-id", "user-id", "tenant-id", "internal", false, true, "pk_inactive", "mist_native")
				mock.ExpectQuery(`WHERE s\.playback_id = \$1`).WithArgs("pk_inactive").WillReturnRows(rows)
			},
			assert: func(t *testing.T, resp *commodorepb.ResolveStreamContextResponse) {
				if resp.Admitted {
					t.Fatalf("expected admitted=false for inactive user")
				}
				if resp.RejectionReason != commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_USER_INACTIVE {
					t.Fatalf("unexpected rejection reason: %v", resp.RejectionReason)
				}
				if resp.StreamId != "stream-id" || resp.TenantId != "tenant-id" {
					t.Fatalf("identity fields not populated on rejection: %+v", resp)
				}
			},
		},
		{
			// nil purserClient ⇒ fail-closed-as-transient. ValidateStreamKey
			// can default to postpaid/active when Purser isn't wired (push
			// ingest re-evaluates every PUSH_REWRITE), but ResolveStreamContext
			// must not admit because the caller is the managed-stream
			// reconciler running every 30s on always-on streams.
			name:    "nil_purser_fails_closed_as_transient",
			req:     &commodorepb.ResolveStreamContextRequest{Identifier: &commodorepb.ResolveStreamContextRequest_InternalName{InternalName: "internal-name-1"}},
			wantErr: true,
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows(cols).
					AddRow("stream-id", "user-id", "tenant-id", "internal-name-1", true, false, "pk_demo", "mist_native")
				mock.ExpectQuery(`WHERE s\.internal_name = \$1`).WithArgs("internal-name-1").WillReturnRows(rows)
			},
		},
		{
			// Fail-closed-as-transient: when a cluster_id is supplied but
			// the cluster route lookup fails (Quartermaster unavailable in
			// this test, since quartermasterClient is nil), the RPC must
			// return codes.Unavailable rather than silently admit. Foghorn's
			// reconciler treats this as transient and preserves any prior
			// applied state instead of newly Applying onto an unverified
			// cluster.
			name:    "cluster_id_with_route_lookup_failure_fails_closed",
			req:     &commodorepb.ResolveStreamContextRequest{Identifier: &commodorepb.ResolveStreamContextRequest_InternalName{InternalName: "internal-name-2"}, ClusterId: "cluster-edge"},
			wantErr: true,
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows(cols).
					AddRow("stream-id", "user-id", "tenant-id", "internal-name-2", true, false, "pk_demo", "mist_native")
				mock.ExpectQuery(`WHERE s\.internal_name = \$1`).WithArgs("internal-name-2").WillReturnRows(rows)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var db *sql.DB
			var mock sqlmock.Sqlmock
			if test.setupMock != nil {
				var err error
				db, mock, err = sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
				if err != nil {
					t.Fatalf("failed to create sqlmock: %v", err)
				}
				defer db.Close()
				test.setupMock(mock)
			}

			server := &CommodoreServer{db: db, logger: logrus.New()}
			resp, err := server.ResolveStreamContext(ctx, test.req)
			if test.wantErr {
				if err == nil {
					t.Fatalf("expected error, got resp=%+v", resp)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			test.assert(t, resp)

			if test.setupMock != nil {
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unmet expectations: %v", err)
				}
			}
		})
	}
}

func TestListManagedStreams(t *testing.T) {
	ctx := context.Background()
	cols := []string{
		"id", "playback_id", "internal_name", "tenant_id",
		"ingest_mode", "source_spec", "source_kind", "always_on",
		"placement_count", "allowed_cluster_ids",
	}

	t.Run("empty_cluster_id_rejected", func(t *testing.T) {
		server := &CommodoreServer{logger: logrus.New()}
		_, err := server.ListManagedStreams(ctx, &commodorepb.ListManagedStreamsRequest{ClusterId: ""})
		if err == nil {
			t.Fatal("expected InvalidArgument for empty cluster_id")
		}
	})

	t.Run("returns_rows_for_cluster", func(t *testing.T) {
		db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()

		// Two rows pinned to cluster-edge: the request scope. A row whose
		// allowed_cluster_ids does NOT include "cluster-edge" would never
		// be returned by the SQL (no $1 = ANY clause match), so the test
		// does not need to mock such a row — that's the new invariant
		// (empty list no longer auto-eligible for every cluster).
		mock.ExpectQuery(`FROM commodore\.streams s.*JOIN commodore\.stream_mist_sources`).
			WithArgs("cluster-edge").
			WillReturnRows(sqlmock.NewRows(cols).
				AddRow(
					"stream-1", "frameworks-demo", "internal-1", "tenant-system",
					"mist_native",
					"ts-exec:ffmpeg -re -stream_loop -1 -i /var/lib/frameworks/demo/clip.mp4 -c copy -f mpegts -",
					"exec", true, int32(1),
					"{cluster-edge}",
				).
				AddRow(
					"stream-2", "linear-2", "internal-2", "tenant-system",
					"mist_native",
					"ts-exec:cat /dev/null",
					"exec", true, int32(2),
					"{cluster-edge}",
				))

		server := &CommodoreServer{db: db, logger: logrus.New()}
		resp, err := server.ListManagedStreams(ctx, &commodorepb.ListManagedStreamsRequest{ClusterId: "cluster-edge"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(resp.Streams) != 2 {
			t.Fatalf("want 2 streams, got %d", len(resp.Streams))
		}
		if resp.Streams[0].GetStreamId() != "stream-1" || resp.Streams[0].GetPlacementCount() != 1 {
			t.Fatalf("stream-1 not populated correctly: %+v", resp.Streams[0])
		}
		if resp.Streams[1].GetPlacementCount() != 2 ||
			len(resp.Streams[1].GetAllowedClusterIds()) != 1 ||
			resp.Streams[1].GetAllowedClusterIds()[0] != "cluster-edge" {
			t.Fatalf("stream-2 not populated correctly: %+v", resp.Streams[1])
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
}

func TestMergeTenantResourceLimitsPreservesPlanCapsWhenOverrideIsPartial(t *testing.T) {
	merged := mergeTenantResourceLimits(
		&quartermasterpb.TenantResourceLimits{MaxStreams: 3, MaxViewers: 200},
		&quartermasterpb.TenantResourceLimits{MaxStreams: 5},
	)
	if merged.GetMaxStreams() != 5 || merged.GetMaxViewers() != 200 {
		t.Fatalf("merged limits = streams:%d viewers:%d, want streams:5 viewers:200",
			merged.GetMaxStreams(), merged.GetMaxViewers())
	}
}

func TestMergeTenantResourceLimitsAllowsOverrideOnly(t *testing.T) {
	merged := mergeTenantResourceLimits(nil, &quartermasterpb.TenantResourceLimits{MaxViewers: 50})
	if merged.GetMaxStreams() != 0 || merged.GetMaxViewers() != 50 {
		t.Fatalf("merged limits = streams:%d viewers:%d, want streams:0 viewers:50",
			merged.GetMaxStreams(), merged.GetMaxViewers())
	}
}

func TestValidateAPIToken(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		req       *commodorepb.ValidateAPITokenRequest
		setupMock func(sqlmock.Sqlmock)
		assert    func(*testing.T, *commodorepb.ValidateAPITokenResponse, error)
	}{
		{
			name: "empty_token",
			req:  &commodorepb.ValidateAPITokenRequest{Token: ""},
			assert: func(t *testing.T, resp *commodorepb.ValidateAPITokenResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resp.Valid {
					t.Fatalf("expected invalid response")
				}
			},
		},
		{
			name: "invalid_token",
			req:  &commodorepb.ValidateAPITokenRequest{Token: "bad-token"},
			setupMock: func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery("FROM commodore.api_tokens").WithArgs(hashToken("bad-token")).WillReturnError(sql.ErrNoRows)
			},
			assert: func(t *testing.T, resp *commodorepb.ValidateAPITokenResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if resp.Valid {
					t.Fatalf("expected invalid response")
				}
			},
		},
		{
			name: "valid_token",
			req:  &commodorepb.ValidateAPITokenRequest{Token: "good-token"},
			setupMock: func(mock sqlmock.Sqlmock) {
				rows := sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "permissions"}).
					AddRow("token-id", "user-id", "tenant-id", "{read,write}")
				mock.ExpectQuery("FROM commodore.api_tokens").WithArgs(hashToken("good-token")).WillReturnRows(rows)
				mock.ExpectExec("UPDATE commodore.api_tokens SET last_used_at").WithArgs("token-id").WillReturnResult(sqlmock.NewResult(1, 1))
				userRows := sqlmock.NewRows([]string{"email", "role"}).AddRow("user@example.com", "admin")
				mock.ExpectQuery("SELECT email, role FROM commodore.users").WithArgs("user-id").WillReturnRows(userRows)
			},
			assert: func(t *testing.T, resp *commodorepb.ValidateAPITokenResponse, err error) {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !resp.Valid {
					t.Fatalf("expected valid response")
				}
				if resp.Email != "user@example.com" || resp.Role != "admin" {
					t.Fatalf("unexpected user details: %q %q", resp.Email, resp.Role)
				}
				if resp.TenantId != "tenant-id" || resp.UserId != "user-id" {
					t.Fatalf("unexpected ids: %q %q", resp.TenantId, resp.UserId)
				}
				if len(resp.Permissions) != 2 || strings.Join(resp.Permissions, ",") != strings.Join([]string{"read", "write"}, ",") {
					t.Fatalf("unexpected permissions: %v", resp.Permissions)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var db *sql.DB
			var mock sqlmock.Sqlmock
			if test.setupMock != nil {
				var err error
				db, mock, err = sqlmock.New()
				if err != nil {
					t.Fatalf("failed to create sqlmock: %v", err)
				}
				defer db.Close()
				test.setupMock(mock)
			}

			server := &CommodoreServer{db: db, logger: logrus.New()}
			resp, err := server.ValidateAPIToken(ctx, test.req)
			test.assert(t, resp, err)

			if test.setupMock != nil {
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatalf("unmet expectations: %v", err)
				}
			}
		})
	}
}

func TestResolveArtifactPlaybackID_PopulatesClusterPeersFromCachedRoute(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"clip_hash", "internal_name", "tenant_id", "user_id", "stream_id", "origin_cluster_id", "requires_auth"}).
		AddRow("clip-hash", "clip-internal", "tenant-1", "user-1", "stream-1", "cluster-origin", false)
	mock.ExpectQuery("FROM commodore.clips").WithArgs("playback-1").WillReturnRows(rows)

	server := &CommodoreServer{
		db:            db,
		logger:        logrus.New(),
		routeCache:    map[string]*clusterRoute{},
		routeCacheTTL: 5 * time.Minute,
	}
	server.routeCache["tenant-1"] = &clusterRoute{
		clusterPeers: []*quartermasterpb.TenantClusterPeer{{ClusterId: "cluster-origin"}},
		resolvedAt:   time.Now(),
	}

	resp, err := server.ResolveArtifactPlaybackID(ctx, &commodorepb.ResolveArtifactPlaybackIDRequest{PlaybackId: "playback-1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Found {
		t.Fatal("expected found response")
	}
	if len(resp.ClusterPeers) != 1 || resp.ClusterPeers[0].GetClusterId() != "cluster-origin" {
		t.Fatalf("expected cluster peers from route cache, got %+v", resp.ClusterPeers)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestValidateStreamKey_OriginClusterUsesIngestClusterWhenProvided(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "internal_name", "is_active", "is_recording_enabled", "playback_id", "ingest_mode"}).
		AddRow("stream-id", "user-id", "tenant-id", "internal", true, true, "pk_test123", "push")
	mock.ExpectQuery("FROM commodore.streams").WithArgs("good-key").WillReturnRows(rows)
	mock.ExpectExec("UPDATE commodore.streams SET active_ingest_cluster_id").WithArgs("cluster-ingest", "good-key").WillReturnResult(sqlmock.NewResult(0, 1))

	server := &CommodoreServer{
		db:     db,
		logger: logrus.New(),
		routeCache: map[string]*clusterRoute{
			"tenant-id": {
				clusterID: "cluster-primary",
				clusterPeers: []*quartermasterpb.TenantClusterPeer{
					{ClusterId: "cluster-primary"},
					{ClusterId: "cluster-ingest"},
				},
				resolvedAt: time.Now(),
			},
		},
		routeCacheTTL: 5 * time.Minute,
	}

	resp, err := server.ValidateStreamKey(context.Background(), &commodorepb.ValidateStreamKeyRequest{
		StreamKey: "good-key",
		ClusterId: "cluster-ingest",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetOriginClusterId() != "cluster-ingest" {
		t.Fatalf("expected origin cluster to match ingest cluster, got %q", resp.GetOriginClusterId())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestValidateStreamKey_UsesMediaClusterWhenFoghornRunsOnPlatformCluster(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed to create sqlmock: %v", err)
	}
	defer db.Close()

	rows := sqlmock.NewRows([]string{"id", "user_id", "tenant_id", "internal_name", "is_active", "is_recording_enabled", "playback_id", "ingest_mode"}).
		AddRow("stream-id", "user-id", "tenant-id", "internal", true, true, "pk_test123", "push")
	mock.ExpectQuery("FROM commodore.streams").WithArgs("good-key").WillReturnRows(rows)
	mock.ExpectExec("UPDATE commodore.streams SET active_ingest_cluster_id").WithArgs("demo-media", "good-key").WillReturnResult(sqlmock.NewResult(0, 1))

	server := &CommodoreServer{
		db:     db,
		logger: logrus.New(),
		routeCache: map[string]*clusterRoute{
			"tenant-id": {
				clusterID: "demo-media",
				clusterPeers: []*quartermasterpb.TenantClusterPeer{
					{ClusterId: "central-primary", ClusterType: "central"},
					{ClusterId: "demo-media", ClusterType: "edge"},
				},
				resolvedAt: time.Now(),
			},
		},
		routeCacheTTL: 5 * time.Minute,
	}

	resp, err := server.ValidateStreamKey(context.Background(), &commodorepb.ValidateStreamKeyRequest{
		StreamKey: "good-key",
		ClusterId: "central-primary",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.GetOriginClusterId() != "demo-media" {
		t.Fatalf("expected origin cluster to stay on media cluster, got %q", resp.GetOriginClusterId())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestSelectActiveIngestCluster(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name      string
		clusterID sql.NullString
		updatedAt sql.NullTime
		wantID    string
		wantOK    bool
	}{
		{
			name:      "fresh cluster",
			clusterID: sql.NullString{String: "cluster-a", Valid: true},
			updatedAt: sql.NullTime{Time: now.Add(-30 * time.Second), Valid: true},
			wantID:    "cluster-a",
			wantOK:    true,
		},
		{
			name:      "stale cluster",
			clusterID: sql.NullString{String: "cluster-a", Valid: true},
			updatedAt: sql.NullTime{Time: now.Add(-(activeIngestClusterFreshnessWindow + time.Second)), Valid: true},
			wantOK:    false,
		},
		{
			name:      "missing timestamp",
			clusterID: sql.NullString{String: "cluster-a", Valid: true},
			updatedAt: sql.NullTime{},
			wantOK:    false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotID, gotOK := selectActiveIngestCluster(tc.clusterID, tc.updatedAt, now)
			if gotOK != tc.wantOK {
				t.Fatalf("expected ok=%v, got %v", tc.wantOK, gotOK)
			}
			if gotID != tc.wantID {
				t.Fatalf("expected cluster id %q, got %q", tc.wantID, gotID)
			}
		})
	}
}
