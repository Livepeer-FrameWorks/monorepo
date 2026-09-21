//go:build schema_verify

package handlers

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerch"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"
)

func countRows(t *testing.T, conn database.ClickHouseNativeConn, query string, args ...any) uint64 {
	t.Helper()
	var n uint64
	if err := conn.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// TestDomainEventProjection_RealClickHouse drives HandleDomainEventMessage and
// the legacy handlers against the current Replicated baseline.
func TestDomainEventProjection_RealClickHouse(t *testing.T) {
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	conn := dockerch.StartCurrent(t, root, "fw-domain-events-ingest").Native
	metrics := domainEventMetrics()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), metrics)
	legacy := NewAnalyticsHandler(conn, logging.NewLogger(), nil)
	ctx := context.Background()
	deliver := func(topic string, ev events.Event) {
		t.Helper()
		if err := h.HandleDomainEventMessage(ctx, domainEventMessage(t, topic, ev)); err != nil {
			t.Fatalf("deliver %s via %s: %v", ev.Type, topic, err)
		}
	}
	tenantID, streamID := uuid.NewString(), uuid.NewString()

	t.Run("local and mirrored copies collapse to one row", func(t *testing.T) {
		const clipHash = "clip-mirrored"
		ready := newDomainEvent(t, tenantID, clipHash, &publicv1.ClipReady{
			Artifact:   &publicv1.Artifact{ArtifactId: clipHash, Kind: publicv1.ArtifactKind_ARTIFACT_KIND_CLIP, StreamId: streamID},
			DurationMs: 30_000, SizeBytes: 1 << 20,
		}, events.WithActor(events.Actor{AuthType: "api_token", UserID: uuid.NewString(), TokenHash: 7}))
		for _, topic := range []string{"domain.events", "us-east.domain.events", "us-east.domain.events"} {
			deliver(topic, ready)
		}

		// Replicated tables drop a byte-identical insert block, so identical
		// copies may already be one base row; the views must hold one either way.
		if base := countRows(t, conn, `SELECT count() FROM periscope.api_events WHERE event_id = ?`, uuid.MustParse(ready.ID)); base < 1 || base > 3 {
			t.Fatalf("api_events base rows = %d, want 1..3", base)
		}
		if got := countRows(t, conn, `SELECT count() FROM periscope.api_events_deduped WHERE event_id = ?`, uuid.MustParse(ready.ID)); got != 1 {
			t.Fatalf("api_events_deduped rows = %d, want 1", got)
		}
		var authType string
		var tokenHash uint64
		if err := conn.QueryRow(ctx, `SELECT actor_auth_type, actor_token_hash FROM periscope.api_events_deduped WHERE event_id = ?`, uuid.MustParse(ready.ID)).Scan(&authType, &tokenHash); err != nil {
			t.Fatal(err)
		}
		if authType != "api_token" || tokenHash != 7 {
			t.Fatalf("actor = (%q, %d), want (api_token, 7)", authType, tokenHash)
		}
		if base := countRows(t, conn, `SELECT count() FROM periscope.artifact_events WHERE event_id = ?`, ready.ID); base < 1 || base > 3 {
			t.Fatalf("artifact_events base rows = %d, want 1..3", base)
		}
		if got := countRows(t, conn, `SELECT count() FROM periscope.artifact_events_deduped WHERE event_id = ?`, ready.ID); got != 1 {
			t.Fatalf("artifact_events_deduped rows = %d, want 1", got)
		}
		if got := countRows(t, conn, `SELECT count() FROM periscope.artifact_state_current_v2 FINAL WHERE tenant_id = ? AND artifact_id = ?`, tenantID, clipHash); got != 1 {
			t.Fatalf("artifact_state_current_v2 rows = %d, want 1", got)
		}
	})

	t.Run("an older aggregate version loses to a newer one", func(t *testing.T) {
		const clipHash = "clip-reordered"
		artifact := &publicv1.Artifact{ArtifactId: clipHash, Kind: publicv1.ArtifactKind_ARTIFACT_KIND_CLIP, StreamId: streamID}
		requested := newDomainEvent(t, tenantID, clipHash, &publicv1.ClipRequested{Artifact: artifact})
		ready := newDomainEvent(t, tenantID, clipHash, &publicv1.ClipReady{Artifact: artifact, SizeBytes: 2048})
		// The newer event lands first; the older one arrives last, as a late
		// mirrored copy, so insertion order alone would keep it.
		deliver("domain.events", ready)
		deliver("us-east.domain.events", requested)

		var stage, eventType string
		if err := conn.QueryRow(ctx, `SELECT stage, event_type FROM periscope.artifact_state_current_v2 FINAL WHERE tenant_id = ? AND artifact_id = ?`, tenantID, clipHash).Scan(&stage, &eventType); err != nil {
			t.Fatal(err)
		}
		if stage != "done" || eventType != "clip.ready" {
			t.Fatalf("state = (%s, %s), want (done, clip.ready)", stage, eventType)
		}

		const uploadHash = "upload-stamped"
		upload := &publicv1.Artifact{ArtifactId: uploadHash, Kind: publicv1.ArtifactKind_ARTIFACT_KIND_UPLOAD}
		// Stamped versions order by aggregate version, not by event ID: the
		// version-2 event is minted first and still wins over version 1.
		failed := newDomainEvent(t, tenantID, uploadHash, &publicv1.UploadFailed{Artifact: upload}, events.WithAggregateVersion(2))
		created := newDomainEvent(t, tenantID, uploadHash, &publicv1.UploadCreated{Artifact: upload, Filename: "talk.mp4"}, events.WithAggregateVersion(1))
		deliver("domain.events", failed)
		deliver("domain.events", created)
		if err := conn.QueryRow(ctx, `SELECT stage, event_type FROM periscope.artifact_state_current_v2 FINAL WHERE tenant_id = ? AND artifact_id = ?`, tenantID, uploadHash).Scan(&stage, &eventType); err != nil {
			t.Fatal(err)
		}
		if stage != "failed" || eventType != "upload.failed" {
			t.Fatalf("stamped state = (%s, %s), want (failed, upload.failed)", stage, eventType)
		}
	})

	t.Run("a legacy copy and a domain copy collapse on the event ID", func(t *testing.T) {
		const clipHash = "clip-dual-written"
		ready := newDomainEvent(t, tenantID, clipHash, &publicv1.ClipReady{
			Artifact: &publicv1.Artifact{ArtifactId: clipHash, Kind: publicv1.ArtifactKind_ARTIFACT_KIND_CLIP, StreamId: streamID},
		})
		sourceMs := ready.Time.UnixMilli()
		trigger := &ipcpb.MistTrigger{
			TriggerType: "clip_lifecycle", NodeId: "edge-1", TenantId: proto.String(tenantID), StreamId: proto.String(streamID),
			TriggerPayload: &ipcpb.MistTrigger_ClipLifecycleData{ClipLifecycleData: &ipcpb.ClipLifecycleData{
				Stage: ipcpb.ClipLifecycleData_STAGE_DONE, ClipHash: clipHash, TenantId: proto.String(tenantID),
				StreamId: proto.String(streamID), SourceUpdatedAtMs: &sourceMs,
			}},
		}
		if err := legacy.HandleAnalyticsEvent(kafka.AnalyticsEvent{
			EventID: ready.ID, EventType: "clip_lifecycle", Timestamp: ready.Time, Source: "foghorn",
			TenantID: tenantID, Data: mustMistTriggerData(t, trigger),
		}); err != nil {
			t.Fatalf("legacy clip lifecycle: %v", err)
		}
		deliver("domain.events", ready)

		if base := countRows(t, conn, `SELECT count() FROM periscope.artifact_events WHERE event_id = ?`, ready.ID); base != 2 {
			t.Fatalf("artifact_events base rows = %d, want 2 (legacy and domain)", base)
		}
		var stage string
		var got uint64
		if err := conn.QueryRow(ctx, `SELECT count(), any(stage) FROM periscope.artifact_events_deduped WHERE event_id = ?`, ready.ID).Scan(&got, &stage); err != nil {
			t.Fatal(err)
		}
		if got != 1 || stage != "done" {
			t.Fatalf("artifact_events_deduped = %d rows with stage %q, want 1 with done", got, stage)
		}

		created := newDomainEvent(t, tenantID, streamID, &publicv1.StreamCreated{StreamId: streamID})
		if err := legacy.HandleServiceEvent(kafka.ServiceEvent{
			EventID: created.ID, EventType: "stream_created", Timestamp: created.Time, Source: "commodore",
			TenantID: tenantID, ResourceType: "stream", ResourceID: streamID,
		}); err != nil {
			t.Fatalf("legacy stream_created: %v", err)
		}
		deliver("domain.events", created)
		if base := countRows(t, conn, `SELECT count() FROM periscope.api_events WHERE event_id = ?`, uuid.MustParse(created.ID)); base != 2 {
			t.Fatalf("api_events base rows = %d, want 2 (legacy and domain)", base)
		}
		if got := countRows(t, conn, `SELECT count() FROM periscope.api_events_deduped WHERE event_id = ?`, uuid.MustParse(created.ID)); got != 1 {
			t.Fatalf("api_events_deduped rows = %d, want 1", got)
		}
	})

	// Each case writes the two copies of one event in both orders, as separate
	// inserts (separate parts), so the view's choice cannot depend on which
	// part it reads first.
	t.Run("the lifecycle row wins in artifact_events_deduped whatever the order", func(t *testing.T) {
		for i, domainFirst := range []bool{true, false, true, false} {
			clipHash := "clip-preferred-" + strconv.Itoa(i)
			ready := newDomainEvent(t, tenantID, clipHash, &publicv1.ClipReady{
				Artifact:  &publicv1.Artifact{ArtifactId: clipHash, Kind: publicv1.ArtifactKind_ARTIFACT_KIND_CLIP, StreamId: streamID},
				SizeBytes: 4096,
			})
			sourceMs := ready.Time.UnixMilli()
			filePath := "/clips/" + clipHash + ".mp4"
			writeLegacy := func() {
				trigger := &ipcpb.MistTrigger{
					TriggerType: "clip_lifecycle", NodeId: "edge-1", TenantId: proto.String(tenantID), StreamId: proto.String(streamID),
					TriggerPayload: &ipcpb.MistTrigger_ClipLifecycleData{ClipLifecycleData: &ipcpb.ClipLifecycleData{
						Stage: ipcpb.ClipLifecycleData_STAGE_DONE, ClipHash: clipHash, TenantId: proto.String(tenantID),
						StreamId: proto.String(streamID), SourceUpdatedAtMs: &sourceMs, FilePath: proto.String(filePath),
						S3Url: proto.String("s3://bucket/" + clipHash), SizeBytes: proto.Uint64(4096),
					}},
				}
				if err := legacy.HandleAnalyticsEvent(kafka.AnalyticsEvent{
					EventID: ready.ID, EventType: "clip_lifecycle", Timestamp: ready.Time, Source: "foghorn",
					TenantID: tenantID, Data: mustMistTriggerData(t, trigger),
				}); err != nil {
					t.Fatalf("legacy clip lifecycle: %v", err)
				}
			}
			if domainFirst {
				deliver("domain.events", ready)
				writeLegacy()
			} else {
				writeLegacy()
				deliver("domain.events", ready)
			}
			var got uint64
			var path, s3URL string
			if err := conn.QueryRow(ctx, `SELECT count(), any(ifNull(file_path, '')), any(ifNull(s3_url, '')) FROM periscope.artifact_events_deduped WHERE event_id = ?`, ready.ID).Scan(&got, &path, &s3URL); err != nil {
				t.Fatal(err)
			}
			if got != 1 || path != filePath || s3URL != "s3://bucket/"+clipHash {
				t.Fatalf("domain first=%v: deduped = %d rows, file_path %q, s3_url %q; want 1 lifecycle row with %q", domainFirst, got, path, s3URL, filePath)
			}
		}

		// A domain row with no lifecycle twin still appears once.
		const onlyDomain = "clip-domain-only"
		ready := newDomainEvent(t, tenantID, onlyDomain, &publicv1.ClipReady{
			Artifact: &publicv1.Artifact{ArtifactId: onlyDomain, Kind: publicv1.ArtifactKind_ARTIFACT_KIND_CLIP, StreamId: streamID},
		})
		deliver("domain.events", ready)
		deliver("us-east.domain.events", ready)
		if got := countRows(t, conn, `SELECT count() FROM periscope.artifact_events_deduped WHERE event_id = ?`, ready.ID); got != 1 {
			t.Fatalf("domain-only event: %d deduped rows, want 1", got)
		}
	})

	t.Run("the domain row wins in api_events_deduped whatever the order", func(t *testing.T) {
		for _, domainFirst := range []bool{true, false, true, false} {
			created := newDomainEvent(t, tenantID, streamID, &publicv1.StreamCreated{StreamId: streamID},
				events.WithActor(events.Actor{AuthType: "jwt", UserID: uuid.NewString(), TokenHash: 99}))
			writeLegacy := func() {
				if err := legacy.HandleServiceEvent(kafka.ServiceEvent{
					EventID: created.ID, EventType: "stream_created", Timestamp: created.Time, Source: "commodore",
					TenantID: tenantID, ResourceType: "stream", ResourceID: streamID,
				}); err != nil {
					t.Fatalf("legacy stream_created: %v", err)
				}
			}
			if domainFirst {
				deliver("domain.events", created)
				writeLegacy()
			} else {
				writeLegacy()
				deliver("domain.events", created)
			}
			var got uint64
			var eventType, authType string
			if err := conn.QueryRow(ctx, `SELECT count(), any(event_type), any(actor_auth_type) FROM periscope.api_events_deduped WHERE event_id = ?`, uuid.MustParse(created.ID)).Scan(&got, &eventType, &authType); err != nil {
				t.Fatal(err)
			}
			if got != 1 || eventType != "stream.created" || authType != "jwt" {
				t.Fatalf("domain first=%v: deduped = %d rows, type %q, actor %q; want 1 row stream.created by jwt", domainFirst, got, eventType, authType)
			}
		}

		// A legacy event with no domain twin keeps its row and name.
		legacyOnly := uuid.New()
		if err := legacy.HandleServiceEvent(kafka.ServiceEvent{
			EventID: legacyOnly.String(), EventType: "artifact_deleted", Timestamp: time.Now().UTC(), Source: "foghorn",
			TenantID: tenantID, ResourceType: "artifact", ResourceID: "clip-x",
		}); err != nil {
			t.Fatalf("legacy artifact_deleted: %v", err)
		}
		var eventType string
		if err := conn.QueryRow(ctx, `SELECT event_type FROM periscope.api_events_deduped WHERE event_id = ?`, legacyOnly).Scan(&eventType); err != nil {
			t.Fatal(err)
		}
		if eventType != "artifact_deleted" {
			t.Fatalf("legacy-only event type %q, want artifact_deleted", eventType)
		}
	})

	t.Run("unknown types are counted and not stored", func(t *testing.T) {
		ev := newDomainEvent(t, tenantID, "clip-unknown", &publicv1.ClipReady{Artifact: &publicv1.Artifact{ArtifactId: "clip-unknown"}})
		msg := domainEventMessage(t, "domain.events", ev)
		msg.Headers[events.HeaderType] = "clip.teleported"
		if err := h.HandleDomainEventMessage(ctx, msg); err != nil {
			t.Fatalf("unknown type: %v", err)
		}
		if got := testutil.ToFloat64(metrics.DomainEvents.WithLabelValues("clip.teleported", domainEventUnknownType)); got != 1 {
			t.Fatalf("unknown_type count = %v, want 1", got)
		}
		for _, table := range []string{"api_events", "artifact_events"} {
			if got := countRows(t, conn, `SELECT count() FROM periscope.`+table+` WHERE toString(event_id) = ?`, ev.ID); got != 0 {
				t.Fatalf("%s holds %d rows of an unknown type", table, got)
			}
		}
		if got := countRows(t, conn, `SELECT count() FROM periscope.artifact_state_current_v2 WHERE artifact_id = 'clip-unknown'`); got != 0 {
			t.Fatalf("artifact_state_current_v2 holds %d rows of an unknown type", got)
		}
	})

	t.Run("the postdeploy seed keeps domain rows and yields to later events", func(t *testing.T) {
		seededAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
		if err := conn.Exec(ctx, `INSERT INTO periscope.artifact_state_current
			(tenant_id, stream_id, request_id, internal_name, content_type, stage, progress_percent, requested_at, updated_at)
			VALUES (?, ?, 'clip-mirrored', 'x', 'clip', 'requested', 0, ?, ?),
			       (?, ?, 'clip-legacy-only', 'x', 'clip', 'queued', 0, ?, ?)`,
			tenantID, streamID, seededAt, seededAt, tenantID, streamID, seededAt, seededAt); err != nil {
			t.Fatal(err)
		}
		seed, err := dbsql.Content.ReadFile("clickhouse/migrations/periscope/v0.3.11/postdeploy/001_seed_artifact_state_current_v2.sql")
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Exec(ctx, strings.TrimSuffix(strings.TrimSpace(string(seed)), ";")); err != nil {
			t.Fatalf("apply seed: %v", err)
		}
		var stage string
		if err := conn.QueryRow(ctx, `SELECT stage FROM periscope.artifact_state_current_v2 FINAL WHERE tenant_id = ? AND artifact_id = 'clip-mirrored'`, tenantID).Scan(&stage); err != nil {
			t.Fatal(err)
		}
		if stage != "done" {
			t.Fatalf("seed replaced a domain row: stage %q, want done", stage)
		}
		if err := conn.QueryRow(ctx, `SELECT stage FROM periscope.artifact_state_current_v2 FINAL WHERE tenant_id = ? AND artifact_id = 'clip-legacy-only'`, tenantID).Scan(&stage); err != nil {
			t.Fatal(err)
		}
		if stage != "queued" {
			t.Fatalf("seeded stage %q, want queued", stage)
		}
		deliver("domain.events", newDomainEvent(t, tenantID, "clip-legacy-only", &publicv1.ClipReady{
			Artifact: &publicv1.Artifact{ArtifactId: "clip-legacy-only", Kind: publicv1.ArtifactKind_ARTIFACT_KIND_CLIP, StreamId: streamID},
		}))
		if err := conn.QueryRow(ctx, `SELECT stage FROM periscope.artifact_state_current_v2 FINAL WHERE tenant_id = ? AND artifact_id = 'clip-legacy-only'`, tenantID).Scan(&stage); err != nil {
			t.Fatal(err)
		}
		if stage != "done" {
			t.Fatalf("a domain event after the seed left stage %q, want done", stage)
		}
	})
}
