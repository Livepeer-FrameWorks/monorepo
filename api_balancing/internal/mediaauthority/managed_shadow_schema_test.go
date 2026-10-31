package mediaauthority

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"
)

func TestManagedSourceMissingTenantIsIncompleteAndFetched(t *testing.T) {
	store, mock, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	metric := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_managed_sets_total", Help: "test"}, []string{"outcome"})
	store.SetManagedStreamSetMetric(metric)
	object := &mediapb.MediaObjectAuthority{TenantId: "tenant", InternalName: "internal",
		Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream", IngestMode: "mist_native"}}}
	payload, err := proto.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	until := store.now().Add(time.Hour)
	mock.ExpectQuery("ListLocalManagedStreamAuthorities").WillReturnRows(sqlmock.NewRows([]string{
		"authority_id", "authority_version", "payload", "payload_sha256", "refresh_after", "valid_until",
		"local_source_ready", "withheld_by_tenant_revival", "tenant_authority_version",
	}).AddRow("stream", 2, payload, digest[:], until, until, true, false, 0))
	mock.ExpectQuery("GetLocalTenantSourceAuthority").WithArgs("tenant").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("BeginMediaAuthorityConfirmation").WillReturnRows(sqlmock.NewRows([]string{"started_at"}).AddRow(store.now()))
	fetched := make(chan AuthorityLookup, 1)
	store.SetAuthorityFetcher(func(_ context.Context, lookup AuthorityLookup) ([][]byte, error) {
		fetched <- lookup
		return nil, nil
	})
	set, err := store.ManagedStreams(context.Background(), "cluster")
	if err != nil || !set.Marked || set.Complete {
		t.Fatalf("missing parent must allow connected fallback without retract: %+v, %v", set, err)
	}
	select {
	case lookup := <-fetched:
		if lookup.AuthorityID != "stream" {
			t.Fatalf("fetched %+v, want the object and parent pair", lookup)
		}
	case <-time.After(time.Second):
		t.Fatal("missing managed-stream parent was not fetched")
	}
	if got := testutil.ToFloat64(metric.WithLabelValues("incomplete")); got != 1 {
		t.Fatalf("incomplete sets = %v, want 1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSourceExpiredTombstoneIsCompleteWithoutParentOrFetch(t *testing.T) {
	store, mock, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	metric := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_managed_sets_total", Help: "test"}, []string{"outcome"})
	store.SetManagedStreamSetMetric(metric)
	object := &mediapb.MediaObjectAuthority{
		TenantId: "tenant", Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE,
		Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream", IngestMode: "mist_native"}},
	}
	payload, err := proto.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	until := store.now().Add(-time.Hour)
	mock.ExpectQuery("ListLocalManagedStreamAuthorities").WillReturnRows(sqlmock.NewRows([]string{
		"authority_id", "authority_version", "payload", "payload_sha256", "refresh_after", "valid_until",
		"local_source_ready", "withheld_by_tenant_revival", "tenant_authority_version",
	}).AddRow("stream", 2, payload, digest[:], until, until, true, true, 0))
	set, err := store.ManagedStreams(context.Background(), "cluster")
	if err != nil || !set.Marked || !set.Complete || len(set.Rows) != 0 {
		t.Fatalf("expired tombstone must be a complete empty desired set: %+v, %v", set, err)
	}
	if got := testutil.ToFloat64(metric.WithLabelValues("complete")); got != 1 {
		t.Fatalf("complete sets = %v, want 1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSourceShadowRejectsPlacementSchemaBeforeSecrets(t *testing.T) {
	for _, schemas := range [][2]uint32{{1, 2}, {2, 1}, {2, 2}} {
		store, mock, closeDB := newFixtureStore(t, "cell-a")
		t.Cleanup(closeDB)
		tenant := &mediapb.TenantAuthority{SchemaVersion: schemas[0], TenantId: "tenant"}
		object := &mediapb.MediaObjectAuthority{SchemaVersion: schemas[1], TenantId: "tenant", InternalName: "internal", ObjectKind: mediapb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
			Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream", IngestMode: "mist_native"}}}
		objectBytes, err := proto.Marshal(object)
		if err != nil {
			t.Fatal(err)
		}
		tenantBytes, err := proto.Marshal(tenant)
		if err != nil {
			t.Fatal(err)
		}
		objectHash, tenantHash := sha256.Sum256(objectBytes), sha256.Sum256(tenantBytes)
		until := store.now().Add(time.Hour)
		mock.ExpectQuery("SELECT authority.payload").WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_id", "authority_version", "local_source_ready", "withheld_by_tenant_revival", "tenant_authority_version"}).
			AddRow(objectBytes, objectHash[:], until, until, sharedauthority.LiveStreamAuthorityID("stream"), 2, false, false, int64(2)))
		mock.ExpectQuery("SELECT authority.payload").WillReturnRows(sqlmock.NewRows([]string{"payload", "payload_sha256", "refresh_after", "valid_until", "authority_version", "local_source_ready"}).
			AddRow(tenantBytes, tenantHash[:], until, until, 2, false))
		got, err := store.PromoteManagedStreamIfMatching(context.Background(), "cluster", &commodorepb.ManagedStreamRow{InternalName: "internal"}, &commodorepb.ResolveStreamContextResponse{Admitted: true})
		if err != nil || got != ManagedStreamPromotionMismatch {
			t.Fatalf("schema %v reached secret access or promotion: %s, %v", schemas, got, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestManagedSourceCancellationStopsTenantReads(t *testing.T) {
	store, mock, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	metric := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_managed_sets_total", Help: "test"}, []string{"outcome"})
	store.SetManagedStreamSetMetric(metric)
	object := &mediapb.MediaObjectAuthority{TenantId: "tenant", InternalName: "internal",
		Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream", IngestMode: "mist_native"}}}
	payload, err := proto.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(payload)
	until := store.now().Add(time.Hour)
	mock.ExpectQuery("ListLocalManagedStreamAuthorities").WillReturnRows(sqlmock.NewRows([]string{
		"authority_id", "authority_version", "payload", "payload_sha256", "refresh_after", "valid_until",
		"local_source_ready", "withheld_by_tenant_revival", "tenant_authority_version",
	}).AddRow("stream", 2, payload, digest[:], until, until, true, false, 0))
	mock.ExpectQuery("GetLocalTenantSourceAuthority").WithArgs("tenant").WillDelayFor(time.Second).WillReturnError(context.Canceled)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err = store.ManagedStreams(ctx, "cluster")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled tenant read returned %v, want caller deadline", err)
	}
	if got := testutil.ToFloat64(metric.WithLabelValues("cancelled")); got != 1 {
		t.Fatalf("cancelled sets = %v, want 1", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSourceDatabaseFailureStopsTenantReads(t *testing.T) {
	for _, parentChanged := range []bool{false, true} {
		t.Run(fmt.Sprintf("parent_changed=%v", parentChanged), func(t *testing.T) {
			store, mock, closeDB := newFixtureStore(t, "cell-a")
			store.SetAuthorityFetcher(func(context.Context, AuthorityLookup) ([][]byte, error) { return nil, errors.New("unexpected refresh") })
			defer closeDB()
			metric := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_managed_sets_total", Help: "test"}, []string{"outcome"})
			store.SetManagedStreamSetMetric(metric)
			object := &mediapb.MediaObjectAuthority{TenantId: "tenant", InternalName: "internal",
				Object: &mediapb.MediaObjectAuthority_LiveStream{LiveStream: &mediapb.LiveStreamAuthority{StreamId: "stream", IngestMode: "mist_native"}}}
			payload, err := proto.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(payload)
			until := store.now().Add(time.Hour)
			mock.ExpectQuery("ListLocalManagedStreamAuthorities").WillReturnRows(sqlmock.NewRows([]string{
				"authority_id", "authority_version", "payload", "payload_sha256", "refresh_after", "valid_until",
				"local_source_ready", "withheld_by_tenant_revival", "tenant_authority_version",
			}).AddRow("stream", 2, payload, digest[:], until, until, true, false, 0).AddRow("stream", 2, payload, digest[:], until, until, true, false, 0))
			dbFault := errors.New("database disconnected")
			if parentChanged {
				tenant := &mediapb.TenantAuthority{TenantId: "tenant"}
				tenantPayload, marshalErr := proto.Marshal(tenant)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				tenantHash := sha256.Sum256(tenantPayload)
				mock.ExpectQuery("GetLocalTenantSourceAuthority").WithArgs("tenant").WillReturnRows(sqlmock.NewRows([]string{
					"payload", "payload_sha256", "refresh_after", "valid_until", "authority_version", "local_source_ready",
				}).AddRow(tenantPayload, tenantHash[:], until, until, 1, true))
				mock.ExpectQuery("GetLocalMediaObjectAuthorityByInternalName").WithArgs("internal").WillReturnError(dbFault)
			} else {
				mock.ExpectQuery("GetLocalTenantSourceAuthority").WithArgs("tenant").WillReturnError(dbFault)
			}
			set, err := store.ManagedStreams(context.Background(), "cluster")
			if set.Complete {
				t.Fatal("database outage returned a complete desired set")
			}
			if !errors.Is(err, dbFault) {
				t.Fatalf("database outage was treated as per-row corruption: %v", err)
			}
			if got := testutil.ToFloat64(metric.WithLabelValues("error")); got != 1 {
				t.Fatalf("failed sets=%v want1", got)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
			store.fetcher.mu.Lock()
			refreshes := len(store.fetcher.remembered)
			store.fetcher.mu.Unlock()
			if refreshes != 0 {
				t.Fatalf("database outage scheduled %d per-row refreshes", refreshes)
			}
		})
	}

}
