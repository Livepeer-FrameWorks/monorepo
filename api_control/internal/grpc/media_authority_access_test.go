package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/protobuf/proto"
)

func TestPlaybackAccessRevocationDirection(t *testing.T) {
	previous := &mediapb.PlaybackPolicy{Kind: mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT, Jwt: &mediapb.PlaybackJwtPolicy{
		ActiveKeys:        []*mediapb.PlaybackSigningKey{{KeyId: "a", PublicKeyPem: "key-a"}},
		RequiredAudiences: []string{"viewer"}, RequiredClaimsJson: map[string]string{"role": `"viewer"`},
	}}
	for _, tt := range []struct {
		name    string
		change  func(*mediapb.PlaybackPolicy)
		revoked bool
	}{
		{"unchanged", func(*mediapb.PlaybackPolicy) {}, false},
		{"add key", func(p *mediapb.PlaybackPolicy) {
			p.Jwt.ActiveKeys = append(p.Jwt.ActiveKeys, &mediapb.PlaybackSigningKey{KeyId: "b"})
		}, false},
		{"remove key", func(p *mediapb.PlaybackPolicy) { p.Jwt.ActiveKeys = nil }, true},
		{"replace key", func(p *mediapb.PlaybackPolicy) { p.Jwt.ActiveKeys[0].PublicKeyPem = "replacement" }, true},
		{"deny", func(p *mediapb.PlaybackPolicy) { p.Kind = mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_DENY }, true},
		{"public", func(p *mediapb.PlaybackPolicy) { p.Kind = mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC }, false},
		{"add audience alternative", func(p *mediapb.PlaybackPolicy) { p.Jwt.RequiredAudiences = append(p.Jwt.RequiredAudiences, "other") }, false},
		{"change audience", func(p *mediapb.PlaybackPolicy) { p.Jwt.RequiredAudiences = []string{"other"} }, true},
		{"clear audience", func(p *mediapb.PlaybackPolicy) { p.Jwt.RequiredAudiences = nil }, false},
		{"remove required claim", func(p *mediapb.PlaybackPolicy) { p.Jwt.RequiredClaimsJson = nil }, false},
		{"add required claim", func(p *mediapb.PlaybackPolicy) { p.Jwt.RequiredClaimsJson["org"] = `"one"` }, true},
		{"disallow old key", func(p *mediapb.PlaybackPolicy) { p.Jwt.AllowedKeyIds = []string{"b"} }, true},
		{"restrict origins", func(p *mediapb.PlaybackPolicy) { p.AllowedOrigins = []string{"https://a.example"} }, true},
		{"allow any origin", func(p *mediapb.PlaybackPolicy) { p.AllowedOrigins = []string{"*"} }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			desired := proto.CloneOf(previous)
			tt.change(desired)
			if got := playbackAccessRevoked(previous, desired); got != tt.revoked {
				t.Fatalf("revoked=%v, want %v", got, tt.revoked)
			}
		})
	}
}

// Removing an allowed origin revokes viewers embedding from it; adding one or
// widening to "*" revokes nothing.
func TestPlaybackOriginRevocationDirection(t *testing.T) {
	previous := &mediapb.PlaybackPolicy{Kind: mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK,
		AllowedOrigins: []string{"https://a.example", "https://b.example"}}
	for _, tt := range []struct {
		name    string
		origins []string
		revoked bool
	}{
		{"add origin", []string{"https://a.example", "https://b.example", "https://c.example"}, false},
		{"remove origin", []string{"https://a.example"}, true},
		{"widen to any", []string{"*"}, false},
		{"clear", nil, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			desired := proto.CloneOf(previous)
			desired.AllowedOrigins = tt.origins
			if got := playbackAccessRevoked(previous, desired); got != tt.revoked {
				t.Fatalf("revoked=%v, want %v", got, tt.revoked)
			}
		})
	}
}

func TestPlacementAccessChangeIgnoresWideningAndRevision(t *testing.T) {
	previous := &pb.PolicySet{Revision: 1, Serve: &pb.Rules{SchemaVersion: 1, Constraints: &pb.Constraints{Allow: &pb.SelectorSet{Any: []*pb.Selector{{ClusterIds: []string{"a"}}}}}}}
	desired := proto.CloneOf(previous)
	desired.Revision++
	if placementAccessChanged(previous, desired) {
		t.Fatal("revision is not access")
	}
	desired.Serve.Constraints.Allow.Any = append(desired.Serve.Constraints.Allow.Any, &pb.Selector{ClusterIds: []string{"b"}})
	if placementAccessChanged(previous, desired) {
		t.Fatal("adding an alternative is not revocation")
	}
	if !placementAccessChanged(desired, previous) {
		t.Fatal("removing an alternative is revocation")
	}
	if placementAccessChanged(previous, &pb.PolicySet{Revision: 3}) {
		t.Fatal("clearing an override is not revocation")
	}
}

func TestCompileFailureFallbackPreservesAwaitTenant(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("GetCurrentMediaAuthorityPayload").WillReturnError(context.DeadlineExceeded)
	failures := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_revocation_failures"}, []string{"stage"})
	registry := prometheus.NewRegistry()
	registry.MustRegister(failures)
	s := &CommodoreServer{db: db, metrics: &ServerMetrics{MediaAuthorityRevocationCheckFailures: failures}}
	err = s.revokeChangedAccessOnCompileFailure(context.Background(), "live_stream:test", mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, denyPlaybackPolicy(), nil, errTenantAuthorityMissing)
	if class, _ := classifyAuthorityCompileError(err); class != authorityCompileAwaitTenant || !errors.Is(err, errTenantAuthorityMissing) {
		t.Fatalf("lost parent wakeup: %v", err)
	}
	metrics, gatherErr := registry.Gather()
	if gatherErr != nil || len(metrics) != 1 || len(metrics[0].Metric) != 1 || metrics[0].Metric[0].GetCounter().GetValue() != 1 {
		t.Fatalf("fallback failure not observable: %v %v", metrics, gatherErr)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPlacementPreferenceRevocationDirection(t *testing.T) {
	previous := &pb.Preferences{Groups: []*pb.Group{{Id: "own", Match: &pb.Selector{ClusterIds: []string{"a"}}, MaxDistanceKm: 100}}}
	for _, tt := range []struct {
		name    string
		change  func(*pb.Preferences)
		revoked bool
	}{
		{"append fallback", func(p *pb.Preferences) { p.Groups = append(p.Groups, &pb.Group{Id: "fallback"}) }, false},
		{"widen selector", func(p *pb.Preferences) { p.Groups[0].Match.ClusterIds = append(p.Groups[0].Match.ClusterIds, "b") }, false},
		{"widen distance", func(p *pb.Preferences) { p.Groups[0].MaxDistanceKm = 200 }, false},
		{"clear distance", func(p *pb.Preferences) { p.Groups[0].MaxDistanceKm = 0 }, false},
		{"rename", func(p *pb.Preferences) { p.Groups[0].Id = "renamed" }, false},
		{"remove groups", func(p *pb.Preferences) { p.Groups = nil }, true},
		{"replace selector", func(p *pb.Preferences) { p.Groups[0].Match.ClusterIds = []string{"b"} }, true},
		{"tighten distance", func(p *pb.Preferences) { p.Groups[0].MaxDistanceKm = 50 }, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			desired := proto.CloneOf(previous)
			tt.change(desired)
			if got := placementPreferencesNarrowed(previous, desired); got != tt.revoked {
				t.Fatalf("revoked=%v, want %v", got, tt.revoked)
			}
		})
	}
	if placementPreferencesNarrowed(previous, nil) || !placementPreferencesNarrowed(nil, previous) {
		t.Fatal("inheritance direction is incorrect")
	}
	if placementPreferencesNarrowed(&pb.Preferences{Groups: []*pb.Group{nil}}, &pb.Preferences{Groups: []*pb.Group{{Id: "all"}}}) {
		t.Fatal("naming an empty group is not a restriction")
	}
}

func TestPlaybackSourceKeepsJWTConstraintDirectionWithoutKeyLookup(t *testing.T) {
	previous := &mediapb.PlaybackPolicy{Kind: mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT, Jwt: &mediapb.PlaybackJwtPolicy{
		ActiveKeys:        []*mediapb.PlaybackSigningKey{{KeyId: "a", PublicKeyPem: "known-key"}},
		RequiredAudiences: []string{"viewer"},
	}}
	for _, tt := range []struct {
		name, document string
		revoked        bool
	}{
		{"same", `{"type":"jwt","jwt":{"required_audience":["viewer"]}}`, false},
		{"wider", `{"type":"jwt","jwt":{"required_audience":["viewer","other"]}}`, false},
		{"narrower", `{"type":"jwt","jwt":{"required_audience":["other"]}}`, true},
		{"key removed from allowlist", `{"type":"jwt","jwt":{"allowed_kids":["b"]}}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := withPlaybackAccessSource(context.Background(), true, tt.document, "")
			desired := ctx.Value(playbackAccessSourceKey{}).(playbackAccessSource).localPolicy(previous)
			if got := playbackAccessRevoked(previous, desired); got != tt.revoked {
				t.Fatalf("revoked=%v, want %v", got, tt.revoked)
			}
		})
	}
}

func TestPlaybackSourceRevisionCanonicalJSON(t *testing.T) {
	revision := func(document string) string {
		return withPlaybackAccessSource(context.Background(), true, document, "ciphertext").Value(playbackAccessSourceKey{}).(playbackAccessSource).revision
	}
	if revision(`{"type":"jwt", "jwt":{}}`) != revision(`{"jwt":{},"type":"jwt"}`) {
		t.Fatal("JSON formatting changed the source revision")
	}
	if revision(`{"value":9007199254740992}`) == revision(`{"value":9007199254740993}`) {
		t.Fatal("source revision lost JSON number precision")
	}
}

func TestCompileAuthorityChecksAccessBeforeTenantLookupSucceeds(t *testing.T) {
	const objectID = "81000000-0000-4000-8000-000000000002"
	const tenantID = "81000000-0000-4000-8000-000000000001"
	previousPolicy := &mediapb.PlaybackPolicy{Kind: mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT, Jwt: &mediapb.PlaybackJwtPolicy{
		ActiveKeys: []*mediapb.PlaybackSigningKey{{KeyId: "a", PublicKeyPem: "known-key"}}, RequiredAudiences: []string{"viewer"},
	}}
	for _, artifact := range []bool{false, true} {
		name := "live"
		if artifact {
			name = "artifact"
		}
		for _, tt := range []struct {
			name, policy string
			revoked      bool
		}{
			{"unchanged", `{"type":"jwt","jwt":{"required_audience":["viewer"]}}`, false},
			{"wider", `{"type":"jwt","jwt":{"required_audience":["viewer","other"]}}`, false},
			{"narrower", `{"type":"jwt","jwt":{"required_audience":["other"]}}`, true},
		} {
			t.Run(name+"/"+tt.name, func(t *testing.T) {
				db, mock, err := sqlmock.New()
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				object := &mediapb.MediaObjectAuthority{SchemaVersion: 1, TenantId: tenantID,
					Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, PlaybackPolicy: previousPolicy}
				authorityID := sharedauthority.LiveStreamAuthorityID(objectID)
				if artifact {
					authorityID = sharedauthority.ArtifactAuthorityID(objectID)
					object.ObjectKind = mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_ARTIFACT
					mock.ExpectQuery("GetArtifactMediaAuthoritySource").WithArgs(objectID).WillReturnRows(sqlmock.NewRows([]string{
						"authority_id", "artifact_kind", "artifact_hash", "tenant_id", "user_id", "stream_id", "internal_name", "playback_id",
						"origin_cluster_id", "requires_auth", "playback_policy", "playback_webhook_secret_enc", "parent_stream_exists", "parent_stream_internal_name", "age_seconds",
					}).AddRow(objectID, "vod", "hash", tenantID, "user", "", "internal", "playback", "", true, tt.policy, "", true, "", 0))
				} else {
					object.ObjectKind = mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM
					object.Object = &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{
						StreamId: objectID, IngestMode: "push", PublishingCredentialSha256: sharedauthority.PublishingCredentialDigest("stream-key"),
					}}
					mock.ExpectQuery("GetLiveStreamMediaAuthoritySource").WithArgs(objectID).WillReturnRows(sqlmock.NewRows([]string{
						"stream_id", "tenant_id", "user_id", "internal_name", "playback_id", "stream_key", "ingest_mode", "requires_auth", "is_recording_enabled",
						"playback_policy", "playback_webhook_secret_enc", "active_ingest_cluster_id", "deleted_at", "age_seconds", "ingest_lease_age_seconds",
					}).AddRow(objectID, tenantID, "user", "internal", "playback", "stream-key", "push", true, false, tt.policy, "", "", nil, 0, 0))
				}
				encoded, err := proto.Marshal(object)
				if err != nil {
					t.Fatal(err)
				}
				tenantErr := errors.New("tenant lookup unavailable")
				denyErr := errors.New("deny publication history unavailable")
				mock.ExpectQuery("GetCurrentMediaAuthorityPayload").WithArgs("tenant", tenantID).WillReturnError(tenantErr)
				mock.ExpectQuery("GetCurrentMediaAuthorityPayload").WithArgs("media_object", authorityID).
					WillReturnRows(sqlmock.NewRows([]string{"payload", "valid_until"}).AddRow(encoded, time.Now().Add(time.Hour)))
				if tt.revoked {
					// Stop at the safety-write entry point; real-engine tests prove the
					// resulting deny is persisted and delivered without a tenant lookup.
					mock.ExpectQuery("GetCurrentMediaAuthorityPayload").WithArgs("media_object", authorityID).WillReturnError(denyErr)
				}
				s := &CommodoreServer{db: db}
				if artifact {
					err = s.compileArtifactAuthority(t.Context(), objectID)
				} else {
					err = s.compileLiveStreamAuthority(t.Context(), objectID)
				}
				if tt.revoked {
					class, code := classifyAuthorityCompileError(err)
					if class != authorityCompileTransient || code != "deny_publish_failed" || !errors.Is(err, denyErr) {
						t.Fatalf("restriction bypassed safety publication after tenant failure: %v", err)
					}
				} else if !errors.Is(err, tenantErr) {
					t.Fatalf("unchanged/wider access lost original tenant failure: %v", err)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestCompileFailureMissingPlacementPreservesAwaitTenant(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	object := &mediapb.MediaObjectAuthority{
		SchemaVersion: 2, TenantId: "81000000-0000-4000-8000-000000000001",
		Lifecycle:      mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		PlaybackPolicy: &mediapb.PlaybackPolicy{Kind: mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_PUBLIC},
		Object:         &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "81000000-0000-4000-8000-000000000002"}},
	}
	encoded, err := proto.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("GetCurrentMediaAuthorityPayload").WillReturnRows(sqlmock.NewRows([]string{"payload", "valid_until"}).AddRow(encoded, time.Now().Add(time.Hour)))
	mock.ExpectBegin()
	mock.ExpectQuery("MediaPlacementStreamExists").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()
	s := &CommodoreServer{db: db}
	err = s.revokeChangedAccessOnCompileFailure(context.Background(), "live_stream:test", mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, object.PlaybackPolicy, nil, errTenantAuthorityMissing)
	if class, _ := classifyAuthorityCompileError(err); class != authorityCompileAwaitTenant {
		t.Fatalf("missing placement burned parent retry budget: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCompileFailureKnownIngestRevocationNeedsNoPlaybackRevisionLookup(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	previous := &mediapb.MediaObjectAuthority{
		Lifecycle:      mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		PlaybackPolicy: &mediapb.PlaybackPolicy{Kind: mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK},
		Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{
			IngestMode: "push", PublishingCredentialSha256: sharedauthority.PublishingCredentialDigest("revoked-key"),
		}},
	}
	encoded, err := proto.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("GetCurrentMediaAuthorityPayload").WillReturnRows(sqlmock.NewRows([]string{"payload", "valid_until"}).AddRow(encoded, time.Now().Add(time.Hour)))
	denyErr := errors.New("deny publication history unavailable")
	mock.ExpectQuery("GetCurrentMediaAuthorityPayload").WillReturnError(denyErr)
	ctx := withPlaybackAccessSource(t.Context(), true, `{"type":"webhook","webhook":{"url":"https://example.test/auth"}}`, "sealed")
	s := &CommodoreServer{db: db}
	err = s.revokeChangedAccessOnCompileFailure(ctx, "live_stream:test", mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
		previous.PlaybackPolicy, &mediapb.LiveStreamAuthority{IngestMode: "push", PublishingCredentialSha256: sharedauthority.PublishingCredentialDigest("new-key")}, errTenantAuthorityMissing)
	if class, code := classifyAuthorityCompileError(err); class != authorityCompileTransient || code != "deny_publish_failed" || !errors.Is(err, denyErr) {
		t.Fatalf("known ingest revocation was blocked by unrelated playback comparison: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCompileFailurePlaybackReadFailureStillChecksPlacement(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	previous := &mediapb.MediaObjectAuthority{
		SchemaVersion: 2, TenantId: "81000000-0000-4000-8000-000000000001",
		Lifecycle:      mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		PlaybackPolicy: &mediapb.PlaybackPolicy{Kind: mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK},
		Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{
			StreamId: "81000000-0000-4000-8000-000000000002",
		}},
	}
	encoded, err := proto.Marshal(previous)
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectQuery("GetCurrentMediaAuthorityPayload").WillReturnRows(sqlmock.NewRows([]string{"payload", "valid_until"}).AddRow(encoded, time.Now().Add(time.Hour)))
	mock.ExpectQuery("GetCurrentMediaAuthorityPlaybackSourceRevision").WillReturnError(errors.New("playback revision unavailable"))
	mock.ExpectBegin()
	mock.ExpectQuery("MediaPlacementStreamExists").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
	mock.ExpectRollback()
	ctx := withPlaybackAccessSource(t.Context(), true, `{"type":"webhook"}`, "sealed")
	s := &CommodoreServer{db: db}
	err = s.revokeChangedAccessOnCompileFailure(ctx, "live_stream:test", mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, previous.PlaybackPolicy, nil, errTenantAuthorityMissing)
	if class, _ := classifyAuthorityCompileError(err); class != authorityCompileAwaitTenant || !errors.Is(err, errTenantAuthorityMissing) {
		t.Fatalf("fallback lost the original parent failure: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
