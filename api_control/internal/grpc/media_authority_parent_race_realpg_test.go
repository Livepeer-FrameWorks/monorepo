//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/auth"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

func TestMediaAuthorityFirstPublicationRechecksParent_RealPG(t *testing.T) {
	testMediaAuthorityFirstPublicationRechecksParent(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityFirstPublicationRechecksParent_RealYugabyte(t *testing.T) {
	testMediaAuthorityFirstPublicationRechecksParent(t, startPlacementDeliveryYugabyte(t, "authority_parent_race"))
}

func testMediaAuthorityFirstPublicationRechecksParent(t *testing.T, db *sql.DB) {
	ctx := context.Background()
	s := &CommodoreServer{db: db, logger: logging.NewLogger(), mediaAuthorityKeyID: "parent-race", mediaAuthorityPrivateKey: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))}
	now := time.Now().UTC()
	parent := obligationTenantPayload()
	revisions := []*mediapb.AuthoritySourceRevision{{Service: "commodore", Revision: "parent-race"}}
	if err := s.persistTenantAuthority(ctx, parent, []string{"cell-a"}, revisions, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	compiling := withMediaObjectParent(ctx, parent)
	parent.AllowPlatformSharedPlayback = !parent.AllowPlatformSharedPlayback
	if err := s.persistTenantAuthority(ctx, parent, []string{"cell-a"}, revisions, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	object := useStreamPayload("first-parent-race")
	const id = "live_stream:first-parent-race"
	object.GetLiveStream().StreamId = "first-parent-race"
	if err := s.persistMediaObjectAuthority(compiling, id, object, []string{"cell-a"}, revisions, now, now.Add(time.Hour)); !errors.Is(err, errMediaAuthorityCompileSuperseded) {
		t.Fatalf("first publication ignored parent change: %v", err)
	}
	_, err := commodoredb.New(db).GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{AuthorityKind: "media_object", AuthorityID: id})
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("stale first publication committed: %v", err)
	}
	if err := s.persistMediaObjectAuthority(withMediaObjectParent(ctx, parent), id, object, []string{"cell-a"}, revisions, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestMediaAuthorityCompileFailurePreservesAccess_RealPG(t *testing.T) {
	testMediaAuthorityCompileFailurePreservesAccess(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityCompileFailurePreservesAccess_RealYugabyte(t *testing.T) {
	testMediaAuthorityCompileFailurePreservesAccess(t, startPlacementDeliveryYugabyte(t, "authority_failure_access"))
}

func testMediaAuthorityCompileFailurePreservesAccess(t *testing.T, db *sql.DB) {
	ctx := context.Background()
	s := &CommodoreServer{db: db, logger: logging.NewLogger(), mediaAuthorityKeyID: "deny", mediaAuthorityPrivateKey: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))}
	now := time.Now().UTC()
	object := useStreamPayload("invalid-policy")
	id := "live_stream:" + object.GetLiveStream().GetStreamId()
	revisions := []*mediapb.AuthoritySourceRevision{{Service: "commodore", Revision: "allow"}}
	if err := s.persistMediaObjectAuthority(ctx, id, object, []string{"cell-a"}, revisions, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	read := func() *mediapb.MediaObjectAuthority {
		t.Helper()
		row, err := commodoredb.New(db).GetCurrentMediaAuthorityPayload(ctx, commodoredb.GetCurrentMediaAuthorityPayloadParams{AuthorityKind: "media_object", AuthorityID: id})
		if err != nil {
			t.Fatal(err)
		}
		payload := &mediapb.MediaObjectAuthority{}
		if err := proto.Unmarshal(row.Payload, payload); err != nil {
			t.Fatal(err)
		}
		return payload
	}
	for _, reason := range []string{"seal_recipient_missing", "source_decrypt_failed", "push_target_decrypt_failed", "playback_decrypt_failed"} {
		err := s.withMediaAuthorityCompileFence(ctx, "media_object:"+id, func(compiling context.Context) error {
			return s.revokeChangedAccessOnCompileFailure(compiling, id, mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, object.GetPlaybackPolicy(), nil, parkAuthorityCompile(reason, errors.New(reason)))
		})
		if class, _ := classifyAuthorityCompileError(err); class != authorityCompilePark || !proto.Equal(read(), object) {
			t.Fatalf("configuration failure %s changed existing authority: %v", reason, err)
		}
	}
	if err := s.withMediaAuthorityCompileFence(ctx, "media_object:"+id, func(compiling context.Context) error {
		return s.persistMediaObjectAuthority(compiling, id, object, []string{"cell-a"}, revisions, now, now.Add(time.Hour))
	}); err != nil {
		t.Fatal(err)
	}
	if read().GetLifecycle() != mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE {
		t.Fatal("a corrected policy could not resume")
	}
	dependencyErr := errors.New("billing unavailable")
	for _, changed := range []bool{false, true} {
		desired := object.GetPlaybackPolicy()
		if changed {
			desired = denyPlaybackPolicy()
		}
		err := s.withMediaAuthorityCompileFence(ctx, "media_object:"+id, func(compiling context.Context) error {
			return s.revokeChangedAccessOnCompileFailure(compiling, id, mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, desired, nil, dependencyErr)
		})
		if !errors.Is(err, dependencyErr) {
			t.Fatalf("lost retryable dependency failure: %v", err)
		}
		inactive := read().GetLifecycle() == mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE
		if inactive != changed {
			t.Fatalf("changed=%v: inactive=%v; only a changed policy should revoke on a dependency outage", changed, inactive)
		}
	}
	_, publicKey, keyID, err := auth.GenerateES256Keypair()
	if err != nil {
		t.Fatal(err)
	}
	object.PlaybackPolicy = &mediapb.PlaybackPolicy{Kind: mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_JWT, Jwt: &mediapb.PlaybackJwtPolicy{
		ActiveKeys: []*mediapb.PlaybackSigningKey{{KeyId: keyID, Algorithm: "ES256", PublicKeyPem: publicKey}},
	}}
	t.Run("stored policy mutation differs from unknown dependency", func(t *testing.T) {
		stored := `{"type":"policy-understood-by-previous-compiler"}`
		validCtx := withPlaybackAccessSource(ctx, true, stored, "")
		if err := s.persistMediaObjectAuthority(validCtx, id, object, []string{"cell-a"}, revisions, now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		for _, changed := range []bool{false, true} {
			checking := validCtx
			encoded := stored
			if changed {
				encoded = `{"type":`
				checking = withPlaybackAccessSource(ctx, true, encoded, "")
			}
			policy, compileErr := s.compilePlaybackPolicy(checking, object.TenantId, true, encoded)
			if policy != nil || compileErr == nil {
				t.Fatal("fixture must exercise the production nil-policy path")
			}
			err := s.withMediaAuthorityCompileFence(checking, "media_object:"+id, func(compiling context.Context) error {
				return s.revokeChangedAccessOnCompileFailure(compiling, id, mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, policy, nil, compileErr)
			})
			if !errors.Is(err, compileErr) || (read().GetLifecycle() == mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE) != changed {
				t.Fatalf("stored change=%v: %v", changed, err)
			}
		}
	})
	t.Run("webhook source rotation with unavailable sealing", func(t *testing.T) {
		webhook := proto.CloneOf(object)
		webhook.PlaybackPolicy = &mediapb.PlaybackPolicy{Kind: mediapb.PlaybackPolicyKind_PLAYBACK_POLICY_KIND_WEBHOOK, ConnectedOnly: true}
		original := `{"type":"webhook","webhook":{"url":"https://example.com/access"}}`
		for _, tt := range []struct {
			name, policy, secret string
			changed              bool
		}{
			{"unchanged", original, "encrypted-a", false},
			{"URL rotation", `{"type":"webhook","webhook":{"url":"https://example.com/new"}}`, "encrypted-a", true},
			{"secret rotation", original, "encrypted-b", true},
		} {
			t.Run(tt.name, func(t *testing.T) {
				if err := s.persistMediaObjectAuthority(withPlaybackAccessSource(ctx, true, original, "encrypted-a"), id, webhook, []string{"cell-a"}, revisions, now, now.Add(time.Hour)); err != nil {
					t.Fatal(err)
				}
				compileErr := parkAuthorityCompile("seal_recipient_missing", errors.New("recipient unavailable"))
				err := s.withMediaAuthorityCompileFence(withPlaybackAccessSource(ctx, true, tt.policy, tt.secret), "media_object:"+id, func(compiling context.Context) error {
					return s.revokeChangedAccessOnCompileFailure(compiling, id, mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, webhook.PlaybackPolicy, nil, compileErr)
				})
				if !errors.Is(err, compileErr) || (read().GetLifecycle() == mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE) != tt.changed {
					t.Fatalf("changed=%v: %v", tt.changed, err)
				}
			})
		}
	})
	if err := s.persistMediaObjectAuthority(ctx, id, object, []string{"cell-a"}, revisions, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	additional := proto.CloneOf(object.GetPlaybackPolicy())
	additional.Jwt.ActiveKeys = append(additional.Jwt.ActiveKeys, &mediapb.PlaybackSigningKey{KeyId: "additional", Algorithm: "ES256", PublicKeyPem: publicKey})
	err = s.withMediaAuthorityCompileFence(ctx, "media_object:"+id, func(compiling context.Context) error {
		return s.revokeChangedAccessOnCompileFailure(compiling, id, mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, additional, nil, dependencyErr)
	})
	if !errors.Is(err, dependencyErr) || !proto.Equal(read(), object) {
		t.Fatalf("adding a tenant JWT key revoked existing playback during dependency outage: %v", err)
	}
	err = s.withMediaAuthorityCompileFence(ctx, "media_object:"+id, func(compiling context.Context) error {
		exhausted, cancel := context.WithCancel(compiling)
		cancel()
		return s.revokeChangedAccessOnCompileFailure(exhausted, id, mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, denyPlaybackPolicy(), nil, context.DeadlineExceeded)
	})
	if !errors.Is(err, context.DeadlineExceeded) || read().GetLifecycle() != mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE {
		t.Fatalf("a dependency consuming the compile deadline prevented an explicit revocation: %v", err)
	}
	object.GetLiveStream().PublishingCredentialSha256 = sharedauthority.PublishingCredentialDigest("old-key")
	object.GetLiveStream().OutageIngestClusterId = "cell-a"
	if err := s.persistMediaObjectAuthority(ctx, id, object, []string{"cell-a"}, revisions, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	err = s.withMediaAuthorityCompileFence(ctx, "media_object:"+id, func(compiling context.Context) error {
		desired := proto.CloneOf(object.GetLiveStream())
		desired.PublishingCredentialSha256 = sharedauthority.PublishingCredentialDigest("rotated-key")
		return s.revokeChangedAccessOnCompileFailure(compiling, id, mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, object.GetPlaybackPolicy(), desired, dependencyErr)
	})
	if !errors.Is(err, dependencyErr) || read().GetLifecycle() != mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_INACTIVE {
		t.Fatalf("old publishing credential survived a rotation blocked by a dependency: %v", err)
	}
	if err := s.persistMediaObjectAuthority(ctx, id, object, []string{"cell-a"}, revisions, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	err = s.withMediaAuthorityCompileFence(ctx, "media_object:"+id, func(compiling context.Context) error {
		if _, err := commodoredb.New(db).BeginMediaAuthorityCompile(ctx, "media_object:"+id); err != nil {
			return err
		}
		return s.revokeChangedAccessOnCompileFailure(compiling, id, mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, denyPlaybackPolicy(), nil, dependencyErr)
	})
	if class, _ := classifyAuthorityCompileError(err); class != authorityCompileSuperseded {
		t.Fatalf("an overtaken deny became an outage or parked a newer compile: %v", err)
	}
}
