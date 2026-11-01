package mediaauthority

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
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

// A spent tenant's row is held on its own: the set stays complete for the cell,
// the stream can keep running where it runs, and suspension is what drops it.
func TestManagedStreamsHoldSpentTenantRowInACompleteSet(t *testing.T) {
	store, mock, closeDB := newFixtureStore(t, "cell-a")
	defer closeDB()
	metric := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_held_managed_sets_total", Help: "test"}, []string{"outcome"})
	store.SetManagedStreamSetMetric(metric)
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SetSealPrivateKey(sharedauthority.SealRecipientKeyID("cell-a", key.PublicKey()), key); err != nil {
		t.Fatal(err)
	}
	const healthyTenant = "10000000-0000-0000-0000-000000000001"
	const spentTenant = "10000000-0000-0000-0000-000000000002"
	const suspendedTenant = "10000000-0000-0000-0000-000000000003"
	const healthyStream = "20000000-0000-0000-0000-000000000001"
	const spentStream = "20000000-0000-0000-0000-000000000002"
	const spentElsewhereStream = "20000000-0000-0000-0000-000000000003"
	const suspendedStream = "20000000-0000-0000-0000-000000000004"
	spentAuthorityID := sharedauthority.LiveStreamAuthorityID(spentStream)

	until := store.now().Add(time.Hour)
	rows := sqlmock.NewRows([]string{
		"authority_id", "authority_version", "payload", "payload_sha256", "refresh_after", "valid_until",
		"local_source_ready", "withheld_by_tenant_revival", "tenant_authority_version",
	})
	var tenantOrder []string
	for _, stream := range []struct{ id, tenant, cluster string }{
		{healthyStream, healthyTenant, "cell-a"},
		{spentStream, spentTenant, "cell-a"},
		{spentElsewhereStream, spentTenant, "cell-b"},
		{suspendedStream, suspendedTenant, "cell-a"},
	} {
		authorityID := sharedauthority.LiveStreamAuthorityID(stream.id)
		object := sealedObjectFixture(t, key, authorityID, &mediapb.LiveStreamSecret{
			AuthorityId: authorityID, TenantId: stream.tenant, NativeSourceSpec: "/media/" + stream.id + ".mp4", NativeSourceKind: "file",
			NativeAlwaysOn: true, NativePlacementCount: 1, NativeAllowedClusterIds: []string{stream.cluster},
		}, nil)
		object.TenantId = stream.tenant
		object.InternalName = "native-" + stream.id
		object.PlaybackId = "playback-" + stream.id
		object.GetLiveStream().StreamId = stream.id
		object.GetLiveStream().IngestMode = "mist_native"
		payload, marshalErr := proto.Marshal(object)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		digest := sha256.Sum256(payload)
		rows.AddRow(authorityID, 2, payload, digest[:], until, until, true, false, 3)
		tenantOrder = append(tenantOrder, stream.tenant)
	}
	mock.ExpectQuery("ListLocalManagedStreamAuthorities").WillReturnRows(rows)
	tenants := map[string]*mediapb.TenantAuthority{
		healthyTenant: {SchemaVersion: sharedauthority.SchemaVersion, TenantId: healthyTenant, Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
			BillingDecision:        mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
			EffectiveClusterGrants: []*mediapb.TenantClusterGrant{{ClusterId: "cell-a"}}},
		spentTenant: {SchemaVersion: sharedauthority.SchemaVersion, TenantId: spentTenant, Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
			BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_PAYMENT_REQUIRED, DecisionReason: sharedauthority.ReasonPrepaidBalanceUnavailable},
		suspendedTenant: {SchemaVersion: sharedauthority.SchemaVersion, TenantId: suspendedTenant, Lifecycle: mediapb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
			BillingDecision: mediapb.TenantBillingDecision_TENANT_BILLING_DECISION_SUSPENDED, DecisionReason: "tenant_suspended"},
	}
	for _, tenantID := range tenantOrder {
		payload, marshalErr := proto.Marshal(tenants[tenantID])
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		digest := sha256.Sum256(payload)
		mock.ExpectQuery("GetLocalTenantSourceAuthority").WithArgs(tenantID).WillReturnRows(sqlmock.NewRows([]string{
			"payload", "payload_sha256", "refresh_after", "valid_until", "authority_version", "local_source_ready",
		}).AddRow(payload, digest[:], until, until, 3, true))
	}

	set, err := store.ManagedStreams(context.Background(), "cell-a")
	if err != nil || !set.Marked || !set.Complete {
		t.Fatalf("one spent tenant made the cell's set incomplete: complete=%v marked=%v err=%v", set.Complete, set.Marked, err)
	}
	if len(set.Rows) != 1 || set.Rows[0].GetStreamId() != healthyStream || set.Contexts[healthyStream] == nil {
		t.Fatalf("desired rows = %+v, want the healthy tenant's stream only", set.Rows)
	}
	if len(set.Held) != 1 || set.Held[spentStream] == nil || set.Contexts[spentStream] != nil {
		t.Fatalf("held rows = %+v, want the spent stream configured for this cluster only", set.Held)
	}
	if !set.HoldsStream("native-"+spentStream) || !set.HoldsStream("PLAYBACK-"+spentStream) || set.HoldsStream("native-"+healthyStream) {
		t.Fatal("held stream lookup does not match by internal name and playback ID alone")
	}
	if got := testutil.ToFloat64(metric.WithLabelValues("complete")); got != 1 {
		t.Fatalf("complete sets = %v, want 1", got)
	}
	renewed := false
	for use := range store.uses.pending {
		renewed = renewed || use.ID == spentAuthorityID
	}
	if !renewed {
		t.Fatal("held always-on stream stopped being renewed")
	}
	if err = mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

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

func TestObjectSourceClassifiesStoreFailure(t *testing.T) {
	for _, cause := range []error{sql.ErrNoRows, errors.New("database disconnected")} {
		t.Run(cause.Error(), func(t *testing.T) {
			store, mock, closeDB := newFixtureStore(t, "cell-a")
			defer closeDB()
			mock.ExpectQuery("GetLocalMediaObjectSourceAuthorityByInternalName").WithArgs("internal").WillReturnError(cause)
			_, err := store.MediaObjectSourceByInternalName(t.Context(), "internal")
			var storeErr *authorityStoreReadError
			if !errors.Is(err, cause) || errors.As(err, &storeErr) == errors.Is(cause, sql.ErrNoRows) {
				t.Fatalf("source read error classification: %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
