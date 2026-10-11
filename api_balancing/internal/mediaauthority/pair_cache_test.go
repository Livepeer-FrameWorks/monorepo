package mediaauthority

import (
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"
)

const pairCacheTenantID = "10000000-0000-0000-0000-000000000001"

var pairCacheColumns = []string{"object_payload", "object_payload_sha256", "object_refresh_after", "object_valid_until", "object_authority_id", "object_authority_version", "object_read_ready", "object_ingest_ready", "object_source_ready", "tenant_payload", "tenant_payload_sha256", "tenant_refresh_after", "tenant_valid_until", "tenant_authority_version", "tenant_read_ready", "tenant_ingest_ready", "tenant_source_ready", "object_withheld_by_tenant_revival"}

type pairCacheFixture struct {
	store *Store
	mock  sqlmock.Sqlmock
	clock atomic.Int64
	reads *prometheus.CounterVec
}

func newPairCacheFixture(t *testing.T) *pairCacheFixture {
	t.Helper()
	store, mock, closeDB := newFixtureStore(t, "cell-a")
	t.Cleanup(closeDB)
	f := &pairCacheFixture{store: store, mock: mock}
	f.clock.Store(storeFixtureNow.Add(time.Minute).UnixNano())
	store.now = func() time.Time { return time.Unix(0, f.clock.Load()).UTC() }
	f.reads = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_placement_pair_reads_total", Help: "test"}, []string{"result"})
	store.SetPlacementPairReadMetric(f.reads)
	return f
}

func (f *pairCacheFixture) now() time.Time { return time.Unix(0, f.clock.Load()).UTC() }

func (f *pairCacheFixture) advance(d time.Duration) { f.clock.Add(int64(d)) }

func (f *pairCacheFixture) count(result string) float64 {
	return testutil.ToFloat64(f.reads.WithLabelValues(result))
}

type pairRowSpec struct {
	streamID      string
	internalName  string
	objectVersion int64
	tenantVersion int64
	objectRefresh time.Time
	objectExpiry  time.Time
}

func (f *pairCacheFixture) spec(streamID, internalName string) pairRowSpec {
	return pairRowSpec{
		streamID: streamID, internalName: internalName, objectVersion: 11, tenantVersion: 6,
		objectRefresh: f.now().Add(time.Hour), objectExpiry: f.now().Add(2 * time.Hour),
	}
}

func pairRow(t *testing.T, spec pairRowSpec) *sqlmock.Rows {
	t.Helper()
	tenant := &mediaauthoritypb.TenantAuthority{SchemaVersion: 2, TenantId: pairCacheTenantID, Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE}
	object := &mediaauthoritypb.MediaObjectAuthority{SchemaVersion: 2, TenantId: pairCacheTenantID, InternalName: spec.internalName,
		ObjectKind: mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM, Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE,
		Object: &mediaauthoritypb.MediaObjectAuthority_LiveStream{LiveStream: &mediaauthoritypb.LiveStreamAuthority{StreamId: spec.streamID, IngestMode: "push"}},
	}
	tenantBytes, err := proto.Marshal(tenant)
	if err != nil {
		t.Fatal(err)
	}
	objectBytes, err := proto.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	tenantExpiry := storeFixtureNow.Add(24 * time.Hour)
	values := []driver.Value{objectBytes, testPayloadDigest(objectBytes), spec.objectRefresh, spec.objectExpiry,
		sharedauthority.LiveStreamAuthorityID(spec.streamID), spec.objectVersion, true, true, true,
		tenantBytes, testPayloadDigest(tenantBytes), tenantExpiry.Add(-time.Hour), tenantExpiry, spec.tenantVersion, true, true, true, false}
	return sqlmock.NewRows(pairCacheColumns).AddRow(values...)
}

func (f *pairCacheFixture) expectPairRead(t *testing.T, spec pairRowSpec) *sqlmock.ExpectedQuery {
	t.Helper()
	return f.mock.ExpectQuery(regexp.QuoteMeta("SELECT object_authority.payload AS object_payload")).
		WithArgs(pairCacheTenantID, sharedauthority.LiveStreamAuthorityID(spec.streamID), spec.internalName).
		WillReturnRows(pairRow(t, spec))
}

func (f *pairCacheFixture) read(t *testing.T, spec pairRowSpec) PlacementPair {
	t.Helper()
	pair, err := f.store.Placement(context.Background(), pairCacheTenantID, sharedauthority.LiveStreamAuthorityID(spec.streamID), spec.internalName)
	if err != nil {
		t.Fatalf("placement read %s: %v", spec.internalName, err)
	}
	return pair
}

// A burst of decisions about one stream reads its pair from the database once.
func TestPairCache_BurstOneQuery(t *testing.T) {
	f := newPairCacheFixture(t)
	spec := f.spec("20000000-0000-0000-0000-000000000001", "burst")
	f.expectPairRead(t, spec).WillDelayFor(100 * time.Millisecond)

	const readers = 100
	start := make(chan struct{})
	var wg sync.WaitGroup
	errs := make(chan error, readers)
	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			pair, err := f.store.Placement(context.Background(), pairCacheTenantID, sharedauthority.LiveStreamAuthorityID(spec.streamID), spec.internalName)
			if err == nil && (pair.Object.Version != spec.objectVersion || pair.Object.Freshness != FreshnessValid) {
				err = errUnexpectedPair
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("burst read: %v", err)
		}
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if f.count("miss") != 1 || f.count("hit") != readers-1 || f.count("bypass") != 0 {
		t.Fatalf("reads hit=%v miss=%v bypass=%v, want 99/1/0", f.count("hit"), f.count("miss"), f.count("bypass"))
	}
}

var errUnexpectedPair = errors.New("unexpected placement pair")

// Callers own what they receive: one caller changing its pair does not change
// another's.
func TestPairCache_CallersReceiveTheirOwnCopy(t *testing.T) {
	f := newPairCacheFixture(t)
	spec := f.spec("20000000-0000-0000-0000-000000000001", "copy")
	f.expectPairRead(t, spec)
	first := f.read(t, spec)
	first.Object.Authority.InternalName = "changed"
	first.Tenant.Authority.TenantId = "changed"
	second := f.read(t, spec)
	if second.Object.Authority.GetInternalName() != "copy" || second.Tenant.Authority.GetTenantId() != pairCacheTenantID {
		t.Fatal("a caller's change reached the kept pair")
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A committed apply on this replica is seen by the next read, with no wait.
func TestPairCache_ApplyInvalidatesImmediately(t *testing.T) {
	f := newPairCacheFixture(t)
	spec := f.spec("20000000-0000-0000-0000-000000000001", "applied")
	f.expectPairRead(t, spec)
	if got := f.read(t, spec); got.Tenant.Version != 6 {
		t.Fatalf("tenant version %d", got.Tenant.Version)
	}
	f.read(t, spec)
	if f.count("hit") != 1 {
		t.Fatalf("second read was not served from the cache: hit=%v", f.count("hit"))
	}

	encoded, _, _ := storeFixture(t, "cell-a")
	expectApplyPrefix(f.mock)
	f.mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.tenant_authority_projection")).WillReturnResult(sqlmock.NewResult(1, 1))
	f.mock.ExpectExec(regexp.QuoteMeta("DELETE FROM foghorn.tenant_authority_grants")).WillReturnResult(sqlmock.NewResult(0, 0))
	f.mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.tenant_authority_grants")).WillReturnResult(sqlmock.NewResult(1, 1))
	f.mock.ExpectExec(regexp.QuoteMeta("INSERT INTO foghorn.media_authority_apply_audit")).WillReturnResult(sqlmock.NewResult(1, 1))
	f.mock.ExpectCommit()
	if _, err := f.store.Apply(context.Background(), encoded); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	applied := spec
	applied.tenantVersion = 7
	f.expectPairRead(t, applied)
	if got := f.read(t, spec); got.Tenant.Version != 7 {
		t.Fatalf("read after a local apply returned tenant version %d, want 7", got.Tenant.Version)
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Another replica's announced apply drops what it touched here: one object for
// an object apply, every pair of the tenant for a tenant apply.
func TestPairCache_PeerAnnounceInvalidates(t *testing.T) {
	f := newPairCacheFixture(t)
	a := f.spec("20000000-0000-0000-0000-00000000000a", "stream-a")
	b := f.spec("20000000-0000-0000-0000-00000000000b", "stream-b")
	f.expectPairRead(t, a)
	f.expectPairRead(t, b)
	f.read(t, a)
	f.read(t, b)
	f.read(t, a)
	f.read(t, b)
	if f.count("hit") != 2 {
		t.Fatalf("hit=%v, want 2", f.count("hit"))
	}

	f.store.ForgetPlacementPairs("media_object", pairCacheTenantID, "stream-a")
	a.objectVersion = 12
	f.expectPairRead(t, a)
	if got := f.read(t, a); got.Object.Version != 12 {
		t.Fatalf("announced object served from the cache: version %d", got.Object.Version)
	}
	f.read(t, b)
	if f.count("hit") != 3 {
		t.Fatalf("an object announcement dropped another object's pair: hit=%v", f.count("hit"))
	}

	f.store.ForgetPlacementPairs("tenant", pairCacheTenantID, "")
	a.tenantVersion, b.tenantVersion = 7, 7
	f.expectPairRead(t, a)
	f.expectPairRead(t, b)
	if f.read(t, a).Tenant.Version != 7 || f.read(t, b).Tenant.Version != 7 {
		t.Fatal("a tenant announcement left a pair of the tenant in the cache")
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Freshness is evaluated by each use, never kept: a kept pair passing
// refresh_after is reported soft-expired at once, and one passing valid_until
// is not answered from the cache.
func TestPairCache_ExpiryEvaluatedAtUse(t *testing.T) {
	f := newPairCacheFixture(t)
	spec := f.spec("20000000-0000-0000-0000-000000000001", "expiring")
	spec.objectRefresh, spec.objectExpiry = f.now().Add(200*time.Millisecond), f.now().Add(500*time.Millisecond)
	f.expectPairRead(t, spec)
	if got := f.read(t, spec); got.Object.Freshness != FreshnessValid {
		t.Fatalf("fresh read %s", got.Object.Freshness)
	}

	f.advance(300 * time.Millisecond)
	if got := f.read(t, spec); got.Object.Freshness != FreshnessSoftExpired {
		t.Fatalf("kept pair past refresh_after reported %s", got.Object.Freshness)
	}
	if f.count("hit") != 1 {
		t.Fatalf("soft-expired pair not served from the cache: hit=%v", f.count("hit"))
	}

	f.advance(300 * time.Millisecond)
	f.expectPairRead(t, spec)
	if got := f.read(t, spec); got.Object.Freshness != FreshnessHardExpired {
		t.Fatalf("kept pair past valid_until reported %s", got.Object.Freshness)
	}
	f.expectPairRead(t, spec)
	if got := f.read(t, spec); got.Object.Freshness != FreshnessHardExpired {
		t.Fatalf("hard-expired read reported %s", got.Object.Freshness)
	}
	if f.count("hit") != 1 || f.count("bypass") != 2 {
		t.Fatalf("hard-expired pair was kept: hit=%v bypass=%v", f.count("hit"), f.count("bypass"))
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Any raise or lower of the restore fence drops every kept pair, including
// pairs the fence would not withhold once lowered.
func TestPairCache_RestoreFenceDrops(t *testing.T) {
	f := newPairCacheFixture(t)
	spec := f.spec("20000000-0000-0000-0000-000000000001", "fenced")
	authorityID := sharedauthority.LiveStreamAuthorityID(spec.streamID)
	fenceAt := func(at time.Time) {
		f.mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(")).WillReturnRows(sqlmock.NewRows([]string{"fenced_at"}).AddRow(at))
	}

	fenceAt(storeFixtureNow)
	if err := f.store.RaiseStartupFence(context.Background()); err != nil {
		t.Fatal(err)
	}
	generation := f.store.fence.confirmationGeneration()
	f.store.fence.confirm("tenant", pairCacheTenantID, generation)
	f.store.fence.confirm("media_object", authorityID, generation)
	f.expectPairRead(t, spec)
	if got := f.read(t, spec); got.Object.Freshness != FreshnessValid {
		t.Fatalf("confirmed pair under the fence reported %s", got.Object.Freshness)
	}
	f.read(t, spec)
	if f.count("hit") != 1 {
		t.Fatalf("confirmed pair not kept: hit=%v", f.count("hit"))
	}

	fenceAt(time.Unix(0, 0))
	if err := f.store.ReloadFence(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.expectPairRead(t, spec)
	f.read(t, spec)
	if f.count("hit") != 1 {
		t.Fatalf("pair kept across a fence lower: hit=%v", f.count("hit"))
	}

	fenceAt(storeFixtureNow.Add(time.Minute))
	if err := f.store.ReloadFence(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.expectPairRead(t, spec)
	if got := f.read(t, spec); got.Object.Freshness != FreshnessHardExpired {
		t.Fatalf("pair read across a fence raise reported %s", got.Object.Freshness)
	}
	if f.count("hit") != 1 {
		t.Fatalf("pair kept across a fence raise: hit=%v", f.count("hit"))
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A kept pair is answered for less than a second after its read began; the
// next read reaches the database.
func TestPairCache_EntryLivesUnderOneSecond(t *testing.T) {
	f := newPairCacheFixture(t)
	spec := f.spec("20000000-0000-0000-0000-000000000001", "ttl")
	f.expectPairRead(t, spec)
	f.read(t, spec)
	f.advance(placementPairCacheTTL - time.Millisecond)
	f.read(t, spec)
	f.advance(time.Millisecond)
	f.expectPairRead(t, spec)
	f.read(t, spec)
	if f.count("hit") != 1 || f.count("miss") != 2 {
		t.Fatalf("hit=%v miss=%v, want 1/2", f.count("hit"), f.count("miss"))
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A read whose pair changed while it was in flight answers its own callers
// and is not kept; a caller arriving after the change starts its own read.
func TestPairCache_ChangeDuringReadIsNotKept(t *testing.T) {
	f := newPairCacheFixture(t)
	spec := f.spec("20000000-0000-0000-0000-000000000001", "racing")
	f.expectPairRead(t, spec).WillDelayFor(100 * time.Millisecond)
	done := make(chan error, 1)
	go func() {
		_, err := f.store.Placement(context.Background(), pairCacheTenantID, sharedauthority.LiveStreamAuthorityID(spec.streamID), spec.internalName)
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for {
		f.store.pairs.mu.Lock()
		inFlight := f.store.pairs.tenants[pairCacheTenantID] != nil && len(f.store.pairs.tenants[pairCacheTenantID].flights) == 1
		f.store.pairs.mu.Unlock()
		if inFlight || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	f.store.ForgetPlacementPairs("media_object", pairCacheTenantID, "racing")
	if err := <-done; err != nil {
		t.Fatalf("read in flight during the change: %v", err)
	}
	f.expectPairRead(t, spec)
	f.read(t, spec)
	if f.count("bypass") != 1 || f.count("miss") != 1 || f.count("hit") != 0 {
		t.Fatalf("hit=%v miss=%v bypass=%v, want 0/1/1", f.count("hit"), f.count("miss"), f.count("bypass"))
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Misses and errors are never kept.
func TestPairCache_MissesAndErrorsAreNotKept(t *testing.T) {
	f := newPairCacheFixture(t)
	spec := f.spec("20000000-0000-0000-0000-000000000001", "absent")
	for range 2 {
		f.mock.ExpectQuery(regexp.QuoteMeta("SELECT object_authority.payload AS object_payload")).WillReturnRows(sqlmock.NewRows(pairCacheColumns))
	}
	for range 2 {
		if _, err := f.store.Placement(context.Background(), pairCacheTenantID, sharedauthority.LiveStreamAuthorityID(spec.streamID), spec.internalName); err == nil {
			t.Fatal("absent pair answered")
		}
	}
	if f.count("bypass") != 2 || f.count("hit") != 0 {
		t.Fatalf("hit=%v bypass=%v, want 0/2", f.count("hit"), f.count("bypass"))
	}
	if err := f.mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
