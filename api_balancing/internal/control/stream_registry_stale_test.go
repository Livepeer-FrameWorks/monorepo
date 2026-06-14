package control

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// Stale-on-transient-error: an expired registry entry is served as fallback
// only when re-hydration fails transiently; authoritative not-found
// (Admitted=false → ErrUnknownStream) always wins so the identity layer's
// negative cache keeps working. See docs/architecture/foghorn-ha.md.

type resolveOutcomes struct {
	mu   sync.Mutex
	list []string
}

func (o *resolveOutcomes) install(r *StreamRegistry) {
	r.SetResolveObserver(func(entity, outcome, _ string) {
		o.mu.Lock()
		o.list = append(o.list, entity+":"+outcome)
		o.mu.Unlock()
	})
}

func (o *resolveOutcomes) has(want string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Contains(o.list, want)
}

func TestResolveSource_StaleServedOnTransientHydrateError(t *testing.T) {
	fake := &fakeCommodore{resp: nativeResp()}
	r := NewStreamRegistry(fake, "cluster-A", 10*time.Millisecond)
	outcomes := &resolveOutcomes{}
	outcomes.install(r)

	if _, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)

	fake.resp, fake.err = nil, errors.New("commodore rpc down")
	e, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992")
	if err != nil {
		t.Fatalf("expected stale entry, got err = %v", err)
	}
	if e.TenantID != "tenant-1" || e.IngestMode != IngestMistNative {
		t.Fatalf("stale entry lost identity: %+v", e)
	}
	if fake.hits != 2 {
		t.Fatalf("Commodore hits = %d, want 2 (revalidation attempted before stale serve)", fake.hits)
	}
	if !outcomes.has("source:stale_served") {
		t.Fatalf("observer outcomes %v missing source:stale_served", outcomes.list)
	}
}

// The live PLAY_REWRITE resolve path reads requires_auth + cluster_peers off
// the registry entry, so hydrate must carry them and stale-serve must preserve
// them through a transient Commodore outage.
func TestResolveSource_CarriesAndStaleServesAuthIdentity(t *testing.T) {
	resp := nativeResp()
	resp.RequiresAuth = true
	resp.ClusterPeers = []*clusterpeerpb.TenantClusterPeer{{ClusterId: "cluster-B"}}
	fake := &fakeCommodore{resp: resp}
	r := NewStreamRegistry(fake, "cluster-A", 10*time.Millisecond)

	e, err := r.ResolveSourceByPlaybackID(context.Background(), "frameworks-demo")
	if err != nil {
		t.Fatal(err)
	}
	if !e.RequiresAuth || !e.RequiresAuthKnown {
		t.Fatalf("hydrate dropped auth bit: %+v", e)
	}
	if len(e.ClusterPeers) != 1 || e.ClusterPeers[0].GetClusterId() != "cluster-B" {
		t.Fatalf("hydrate dropped cluster peers: %+v", e.ClusterPeers)
	}

	time.Sleep(20 * time.Millisecond)
	fake.resp, fake.err = nil, errors.New("commodore rpc down")
	stale, err := r.ResolveSourceByPlaybackID(context.Background(), "frameworks-demo")
	if err != nil {
		t.Fatalf("expected stale entry, got err = %v", err)
	}
	if !stale.RequiresAuth || !stale.RequiresAuthKnown || len(stale.ClusterPeers) != 1 {
		t.Fatalf("stale serve lost auth identity: %+v", stale)
	}
}

// A routing-only warm entry (as PUSH_REWRITE admission / managed-stream upsert
// creates it — fresh, but no RequiresAuthKnown) must be hydrated on the live
// playback-resolve path so the auth identity is filled, then served fresh
// without re-hydrating once complete. STREAM_SOURCE (requireAuth=false) keeps
// short-circuiting on the same warm entry.
func TestResolveSource_HydratesWarmEntryMissingAuth(t *testing.T) {
	resp := nativeResp()
	resp.RequiresAuth = true
	fake := &fakeCommodore{resp: resp}
	r := NewStreamRegistry(fake, "cluster-A", time.Minute)

	ce := &cachedEntry{entry: StreamEntry{
		PlaybackID:   "frameworks-demo",
		InternalName: "60546679b497415db2338cd5cae54992",
		RuntimeName:  "60546679b497415db2338cd5cae54992",
		IngestMode:   IngestMistNative,
		TenantID:     "tenant-1",
		// no RequiresAuthKnown / ClusterPeers — routing only
	}, cached: time.Now()}
	r.mu.Lock()
	r.byPlay["frameworks-demo"] = ce
	r.byInt["60546679b497415db2338cd5cae54992"] = ce
	r.mu.Unlock()

	// requireAuth=false (internal-name path) short-circuits the warm entry.
	if _, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992"); err != nil {
		t.Fatal(err)
	}
	if fake.hits != 0 {
		t.Fatalf("STREAM_SOURCE path must not hydrate a fresh entry, hits=%d", fake.hits)
	}

	// requireAuth=true (playback path) hydrates to fill the auth identity.
	e, err := r.ResolveSourceByPlaybackID(context.Background(), "frameworks-demo")
	if err != nil {
		t.Fatal(err)
	}
	if fake.hits != 1 {
		t.Fatalf("warm authless entry must hydrate on the playback path, hits=%d", fake.hits)
	}
	if !e.RequiresAuthKnown || !e.RequiresAuth {
		t.Fatalf("hydrate did not fill auth identity: %+v", e)
	}

	// Now fresh WITH auth → no second hydrate.
	if _, err := r.ResolveSourceByPlaybackID(context.Background(), "frameworks-demo"); err != nil {
		t.Fatal(err)
	}
	if fake.hits != 1 {
		t.Fatalf("auth-complete fresh entry must not re-hydrate, hits=%d", fake.hits)
	}
}

func TestResolveSource_AuthoritativeNotFoundNotServedStale(t *testing.T) {
	fake := &fakeCommodore{resp: nativeResp()}
	r := NewStreamRegistry(fake, "cluster-A", 10*time.Millisecond)

	if _, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)

	// Commodore answers authoritatively: stream retracted.
	fake.resp, fake.err = &commodorepb.ResolveStreamContextResponse{Admitted: false}, nil
	_, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992")
	if !errors.Is(err, ErrUnknownStream) {
		t.Fatalf("err = %v, want ErrUnknownStream (stale must not mask authoritative not-found)", err)
	}
}

func TestResolveSource_StaleCapExceeded(t *testing.T) {
	fake := &fakeCommodore{resp: nativeResp()}
	r := NewStreamRegistry(fake, "cluster-A", 10*time.Millisecond)
	r.SetStaleMax(5 * time.Millisecond)

	if _, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(25 * time.Millisecond) // past ttl+staleMax

	fake.resp, fake.err = nil, errors.New("commodore rpc down")
	if _, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992"); err == nil {
		t.Fatal("entry older than ttl+staleMax must not be served")
	}
}

// Commodore not connected (boot window) is transient — ErrRegistryUnavailable,
// never ErrUnknownStream — so the identity layer won't negative-cache it. With
// a stale snapshot in hand, the lookup still answers.
func TestResolveSource_NilClientUnavailableAndStaleServe(t *testing.T) {
	fake := &fakeCommodore{resp: nativeResp()}
	r := NewStreamRegistry(fake, "cluster-A", 10*time.Millisecond)
	outcomes := &resolveOutcomes{}
	outcomes.install(r)

	if _, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)

	r.SetCommodoreClient(nil)
	e, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992")
	if err != nil || e.TenantID != "tenant-1" {
		t.Fatalf("stale serve with nil client failed: e=%+v err=%v", e, err)
	}

	// Without anything cached, nil client is a plain unavailable error.
	_, err = r.ResolveSourceByInternalName(context.Background(), "never-seen")
	if !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("err = %v, want ErrRegistryUnavailable", err)
	}
	if !outcomes.has("source:unavailable") {
		t.Fatalf("observer outcomes %v missing source:unavailable", outcomes.list)
	}
}

// All three key paths share the stale-serving resolve helper.
func TestResolveSource_StaleServedOnAllKeyPaths(t *testing.T) {
	fake := &fakeCommodore{resp: nativeResp()}
	r := NewStreamRegistry(fake, "cluster-A", 10*time.Millisecond)

	if _, err := r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	fake.resp, fake.err = nil, errors.New("commodore rpc down")

	cases := []struct {
		name    string
		resolve func() (StreamEntry, error)
	}{
		{"internal_name", func() (StreamEntry, error) {
			return r.ResolveSourceByInternalName(context.Background(), "60546679b497415db2338cd5cae54992")
		}},
		{"playback_id", func() (StreamEntry, error) {
			return r.ResolveSourceByPlaybackID(context.Background(), "frameworks-demo")
		}},
		{"stream_id", func() (StreamEntry, error) {
			return r.ResolveSourceByStreamID(context.Background(), "stream-uuid-1")
		}},
	}
	for _, tc := range cases {
		e, err := tc.resolve()
		if err != nil || e.TenantID != "tenant-1" {
			t.Fatalf("%s: stale serve failed: e=%+v err=%v", tc.name, e, err)
		}
	}
}

// ctxAwareCommodore fails the RPC when the context it receives is already
// done — the seam for proving hydrate runs detached from caller cancellation.
type ctxAwareCommodore struct {
	resp *commodorepb.ResolveStreamContextResponse
	hits int
}

func (f *ctxAwareCommodore) ResolveStreamContext(ctx context.Context, _, _, _, _ string) (*commodorepb.ResolveStreamContextResponse, error) {
	f.hits++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.resp, nil
}

// hydrate is singleflight-shared: an abandoned first caller must not fail
// the round for every concurrent waiter, so the RPC runs on a context
// detached from the caller's cancellation (with its own timeout).
func TestResolveSource_HydrateDetachedFromCallerCancellation(t *testing.T) {
	fake := &ctxAwareCommodore{resp: nativeResp()}
	r := NewStreamRegistry(fake, "cluster-A", time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // caller already gone

	e, err := r.ResolveSourceByInternalName(ctx, "60546679b497415db2338cd5cae54992")
	if err != nil {
		t.Fatalf("hydrate with canceled caller ctx failed: %v (must be detached)", err)
	}
	if e.TenantID != "tenant-1" || fake.hits != 1 {
		t.Fatalf("entry=%+v hits=%d, want tenant-1 via one RPC", e, fake.hits)
	}
}

func TestResolveArtifact_StaleServedOnTransientSQLError(t *testing.T) {
	tdb, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatal(err)
	}
	defer tdb.Close()

	mock.ExpectQuery(`SELECT artifact_hash, artifact_type`).
		WithArgs(sampleHash).
		WillReturnRows(sqlmock.NewRows([]string{
			"artifact_hash", "artifact_type", "internal_name", "stream_internal_name",
			"stream_id", "tenant_id", "status", "format",
			"origin_cluster_id", "storage_cluster_id", "has_thumbnails",
		}).AddRow(sampleHash, "vod", sampleVODInternal, "src_internal", "", "tenant-1", "ready", "mp4", "", "", false))

	r := NewStreamRegistry(nil, "cluster-A", 10*time.Millisecond)
	if _, err := r.ResolveArtifactByHash(context.Background(), tdb, sampleHash); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)

	// Transient SQL failure → stale entry served.
	mock.ExpectQuery(`SELECT artifact_hash, artifact_type`).
		WithArgs(sampleHash).
		WillReturnError(errors.New("pg connection refused"))
	e, resolveErr := r.ResolveArtifactByHash(context.Background(), tdb, sampleHash)
	if resolveErr != nil || e.TenantID != "tenant-1" {
		t.Fatalf("stale artifact serve failed: e=%+v err=%v", e, resolveErr)
	}

	// Authoritative no-row → miss, despite the stale entry.
	time.Sleep(20 * time.Millisecond)
	mock.ExpectQuery(`SELECT artifact_hash, artifact_type`).
		WithArgs(sampleHash).
		WillReturnRows(sqlmock.NewRows([]string{}))
	if _, resolveErr := r.ResolveArtifactByHash(context.Background(), tdb, sampleHash); !errors.Is(resolveErr, ErrUnknownArtifact) {
		t.Fatalf("err = %v, want ErrUnknownArtifact over stale entry", resolveErr)
	}
}
