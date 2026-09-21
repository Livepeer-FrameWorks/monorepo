package mediaauthority

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func fetchTestStore(now *time.Time) *Store {
	return &Store{
		now:     func() time.Time { return *now },
		refresh: &refreshCoordinator{}, fetcher: &fetchCoordinator{}, uses: newUseRecorder(),
	}
}

// A decision about an object the cell does not hold waits for a fetch. Every one
// of these bounds exists so that the wait is paid rarely: never twice for a name
// that does not exist, never at all while the control plane is unreachable, and
// once for any number of decisions made at the same moment.
func TestFetchBoundsWhatADecisionWaitsFor(t *testing.T) {
	lookup := AuthorityLookup{PlaybackID: "playback-1"}

	t.Run("no fetcher installed", func(t *testing.T) {
		now := storeFixtureNow
		if applied, err := fetchTestStore(&now).Fetch(context.Background(), lookup); applied || !errors.Is(err, ErrAuthorityFetchUnavailable) {
			t.Fatalf("applied=%v err=%v, want unavailable", applied, err)
		}
	})

	t.Run("an object that does not exist is asked for once per window", func(t *testing.T) {
		now := storeFixtureNow
		store := fetchTestStore(&now)
		var calls atomic.Int32
		store.SetAuthorityFetcher(func(context.Context, AuthorityLookup) ([][]byte, error) {
			calls.Add(1)
			return nil, status.Error(codes.NotFound, "media object not found")
		})
		for range 3 {
			if applied, err := store.Fetch(context.Background(), lookup); applied || !errors.Is(err, ErrAuthorityFetchUnavailable) {
				t.Fatalf("applied=%v err=%v, want unavailable", applied, err)
			}
		}
		if calls.Load() != 1 {
			t.Fatalf("a name that does not exist was asked for %d times inside the window", calls.Load())
		}
		if _, err := store.Fetch(context.Background(), AuthorityLookup{PlaybackID: "playback-2"}); !errors.Is(err, ErrAuthorityFetchUnavailable) || calls.Load() != 2 {
			t.Fatalf("a different name was not asked for: calls=%d err=%v", calls.Load(), err)
		}
		now = now.Add(authorityFetchNotFoundTTL + time.Second)
		_, _ = store.Fetch(context.Background(), lookup)
		if calls.Load() != 3 {
			t.Fatalf("calls after the window = %d, want 3", calls.Load())
		}
	})

	t.Run("an unreachable control plane stops every decision from waiting", func(t *testing.T) {
		now := storeFixtureNow
		store := fetchTestStore(&now)
		var calls atomic.Int32
		store.SetAuthorityFetcher(func(context.Context, AuthorityLookup) ([][]byte, error) {
			calls.Add(1)
			return nil, status.Error(codes.Unavailable, "commodore down")
		})
		for _, name := range []string{"a", "b", "c"} {
			_, _ = store.Fetch(context.Background(), AuthorityLookup{InternalName: name})
		}
		if calls.Load() != 1 {
			t.Fatalf("decisions kept waiting on an unreachable control plane: %d fetches", calls.Load())
		}
		now = now.Add(authorityFetchOutageBackoff + time.Second)
		_, _ = store.Fetch(context.Background(), AuthorityLookup{InternalName: "a"})
		if calls.Load() != 2 {
			t.Fatalf("fetching did not resume after the backoff: %d fetches", calls.Load())
		}
	})

	t.Run("compile contention does not back off unrelated authorities", func(t *testing.T) {
		now := storeFixtureNow
		store := fetchTestStore(&now)
		calls := 0
		store.SetAuthorityFetcher(func(context.Context, AuthorityLookup) ([][]byte, error) {
			calls++
			return nil, status.Error(codes.Aborted, "compile superseded")
		})
		for _, name := range []string{"a", "b", "a"} {
			_, _ = store.Fetch(context.Background(), AuthorityLookup{InternalName: name})
		}
		if calls != 3 {
			t.Fatalf("contention opened shared backoff: %d calls", calls)
		}
	})

	t.Run("a core upgrade resumes fetching on the same connection", func(t *testing.T) {
		now := storeFixtureNow
		store := fetchTestStore(&now)
		var calls atomic.Int32
		store.SetAuthorityFetcher(func(context.Context, AuthorityLookup) ([][]byte, error) {
			calls.Add(1)
			return nil, status.Error(codes.Unimplemented, "unknown method FetchMediaAuthority")
		})
		_, _ = store.Fetch(context.Background(), lookup)
		_, _ = store.Fetch(context.Background(), AuthorityLookup{PlaybackID: "playback-2"})
		if calls.Load() != 1 {
			t.Fatalf("unsupported core was asked during backoff: %d", calls.Load())
		}
		now = now.Add(authorityFetchUnsupportedBackoff + time.Second)
		_, _ = store.Fetch(context.Background(), AuthorityLookup{PlaybackID: "playback-2"})
		if calls.Load() != 2 {
			t.Fatalf("core was not retried after backoff: %d", calls.Load())
		}
	})

	t.Run("decisions made together share one fetch", func(t *testing.T) {
		now := storeFixtureNow
		store := fetchTestStore(&now)
		var calls atomic.Int32
		release := make(chan struct{})
		store.SetAuthorityFetcher(func(context.Context, AuthorityLookup) ([][]byte, error) {
			calls.Add(1)
			<-release
			return nil, nil
		})
		var waiting sync.WaitGroup
		for range 8 {
			waiting.Add(1)
			go func() {
				defer waiting.Done()
				_, _ = store.Fetch(context.Background(), lookup)
			}()
		}
		time.Sleep(50 * time.Millisecond)
		close(release)
		waiting.Wait()
		if calls.Load() != 1 {
			t.Fatalf("eight concurrent decisions made %d fetches", calls.Load())
		}
	})
}

// Federation discovery, admission and ingest resolution decide on the placement
// pair alone. A cell a request reaches that holds nothing for the object, or
// holds it past its validity, asks for it and decides on what comes back; a
// tombstone is an answer and is never asked around.
func TestPlacementPairIsFetchedOnMissOrExpiry(t *testing.T) {
	now := storeFixtureNow
	valid := PlacementPair{
		Object: MediaObjectSnapshot{Authority: &mediaauthoritypb.MediaObjectAuthority{Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE}, ValidUntil: now.Add(time.Hour)},
		Tenant: TenantSnapshot{Authority: &mediaauthoritypb.TenantAuthority{Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE}, ValidUntil: now.Add(time.Hour)},
	}
	expiredTenant := valid
	expiredTenant.Tenant.ValidUntil = now.Add(-time.Minute)
	tombstone := valid
	tombstone.Object.Authority = &mediaauthoritypb.MediaObjectAuthority{Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE}
	tombstone.Object.ValidUntil = now.Add(-time.Hour)

	for name, tc := range map[string]struct {
		first     PlacementPair
		firstErr  error
		wantFetch bool
	}{
		"absent":            {firstErr: sql.ErrNoRows, wantFetch: true},
		"tenant expired":    {first: expiredTenant, wantFetch: true},
		"valid":             {first: valid},
		"expired tombstone": {first: tombstone},
		"unrelated failure": {firstErr: errors.New("connection reset")},
	} {
		t.Run(name, func(t *testing.T) {
			store := fetchTestStore(&now)
			var fetched []AuthorityLookup
			store.SetAuthorityFetcher(func(_ context.Context, lookup AuthorityLookup) ([][]byte, error) {
				fetched = append(fetched, lookup)
				return nil, nil
			})
			// A fetch that brings nothing leaves the first read standing.
			reads := 0
			pair, err := store.readPlacementFetchingOnMiss(context.Background(), AuthorityLookup{InternalName: "stream"}, func(context.Context) (PlacementPair, error) {
				reads++
				return tc.first, tc.firstErr
			})
			if (len(fetched) == 1) != tc.wantFetch || reads != 1 {
				t.Fatalf("fetched %v, read %d time(s); want fetch=%v and a single read when nothing came back", fetched, reads, tc.wantFetch)
			}
			if !errors.Is(err, tc.firstErr) || pair.Object.ValidUntil != tc.first.Object.ValidUntil {
				t.Fatalf("read after a fetch that brought nothing = %+v, %v; want the first read unchanged", pair, err)
			}
		})
	}
}

// Every decision that asks is counted once, whichever path asked: the
// placement read and a direct caller use the same counter, a failure is
// unavailable, a skipped ask during an outage is unavailable too, and a cell
// with no control plane to ask is not counted as failing.
func TestEveryFetchIsCountedOnce(t *testing.T) {
	now := storeFixtureNow
	store := fetchTestStore(&now)
	outcomes := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "fetches_total"}, []string{"outcome"})
	store.SetFetchOutcomeMetric(outcomes)
	count := func(outcome string) float64 { return testutil.ToFloat64(outcomes.WithLabelValues(outcome)) }

	if _, err := store.Fetch(context.Background(), AuthorityLookup{AuthorityID: "a"}); err == nil {
		t.Fatal("fetch without a fetcher succeeded")
	}
	if count("not_installed") != 1 || count("unavailable") != 0 {
		t.Fatalf("no fetcher: not_installed=%v unavailable=%v", count("not_installed"), count("unavailable"))
	}

	store.SetAuthorityFetcher(func(context.Context, AuthorityLookup) ([][]byte, error) {
		return nil, status.Error(codes.Unavailable, "down")
	})
	// Through the placement read.
	_, _ = store.readPlacementFetchingOnMiss(context.Background(), AuthorityLookup{InternalName: "stream"}, func(context.Context) (PlacementPair, error) { //nolint:errcheck // only the count matters
		return PlacementPair{}, sql.ErrNoRows
	})
	if count("unavailable") != 1 {
		t.Fatalf("placement read failure counted %v, want 1", count("unavailable"))
	}
	// A direct caller during the outage backoff is not asked through, and is
	// still a decision that could not fetch.
	_, _ = store.Fetch(context.Background(), AuthorityLookup{AuthorityID: "b"}) //nolint:errcheck // only the count matters
	if count("unavailable") != 2 {
		t.Fatalf("fetch during the outage backoff counted %v unavailable in total, want 2", count("unavailable"))
	}

	now = now.Add(time.Minute)
	store.SetAuthorityFetcher(func(context.Context, AuthorityLookup) ([][]byte, error) { return nil, nil })
	_, _ = store.Fetch(context.Background(), AuthorityLookup{AuthorityID: "c"}) //nolint:errcheck // only the count matters
	_, _ = store.Fetch(context.Background(), AuthorityLookup{AuthorityID: "c"}) //nolint:errcheck // remembered: nothing
	if count("nothing") != 2 || count("unavailable") != 2 {
		t.Fatalf("nothing for this cell: nothing=%v unavailable=%v, want 2 and 2", count("nothing"), count("unavailable"))
	}
	store.SetAuthorityFetcher(func(context.Context, AuthorityLookup) ([][]byte, error) {
		return nil, status.Error(codes.NotFound, "unknown playback ID")
	})
	_, _ = store.Fetch(context.Background(), AuthorityLookup{PlaybackID: "scanner"})
	_, _ = store.Fetch(context.Background(), AuthorityLookup{PlaybackID: "scanner"})
	if count("nothing") != 4 || count("unavailable") != 2 {
		t.Fatalf("negative lookup counted as outage: nothing=%v unavailable=%v", count("nothing"), count("unavailable"))
	}
}

func TestBackgroundFetchConcurrencyIsBounded(t *testing.T) {
	now := storeFixtureNow
	store := fetchTestStore(&now)
	started := make(chan struct{}, 100)
	release := make(chan struct{})
	defer close(release)
	store.SetAuthorityFetcher(func(context.Context, AuthorityLookup) ([][]byte, error) {
		started <- struct{}{}
		<-release
		return nil, nil
	})
	for i := range 100 {
		store.RefreshAuthorityAsync(AuthorityLookup{AuthorityID: fmt.Sprint(i)})
	}
	for range authorityFetchAsyncLimit {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("background fetch slot did not start")
		}
	}
	store.fetcher.mu.Lock()
	active := store.fetcher.asyncActive
	store.fetcher.mu.Unlock()
	if active != authorityFetchAsyncLimit || len(started) != 0 {
		t.Fatalf("unbounded background requests: active=%d extra=%d", active, len(started))
	}
}

// A fetch is shared by every decision waiting on the same authority. One whose
// own deadline ends stops waiting at once; the fetch is not cancelled for the
// others, and runs once.
func TestFetchWaiterLeavesWithoutCancellingTheSharedFetch(t *testing.T) {
	now := storeFixtureNow
	store := fetchTestStore(&now)
	release := make(chan struct{})
	var calls atomic.Int32
	store.SetAuthorityFetcher(func(ctx context.Context, _ AuthorityLookup) ([][]byte, error) {
		calls.Add(1)
		select {
		case <-release:
			return nil, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	lookup := AuthorityLookup{AuthorityID: "live_stream:stream"}
	patient := make(chan error, 1)
	go func() {
		_, err := store.Fetch(context.Background(), lookup)
		patient <- err
	}()
	for calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	hurried, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := store.Fetch(hurried, lookup); !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrAuthorityFetchUnavailable) {
		t.Fatalf("waiter past its deadline returned %v, want its own deadline as unavailable", err)
	}
	close(release)
	if err := <-patient; err != nil {
		t.Fatalf("the remaining waiter lost the shared fetch: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("fetched %d times, want once", calls.Load())
	}
}

// A soft-expired read means this one authority's renewal is overdue. It asks for
// that authority, in the background and at most once a minute, instead of asking
// the control plane to replay everything the cell holds.
func TestSoftExpiryAsksForThatAuthorityNotForTheWholeCell(t *testing.T) {
	now := storeFixtureNow
	store := fetchTestStore(&now)
	asked := make(chan AuthorityLookup, 4)
	store.SetAuthorityFetcher(func(_ context.Context, lookup AuthorityLookup) ([][]byte, error) {
		asked <- lookup
		return nil, nil
	})
	store.SetRefreshRequester(func(context.Context) error {
		t.Error("a soft-expired read asked for a replay of the whole cell")
		return nil
	})
	lookup := AuthorityLookup{AuthorityID: "live_stream:stream-1"}
	store.observeFreshness(FreshnessSoftExpired, lookup)
	store.observeFreshness(FreshnessSoftExpired, lookup)
	store.observeFreshness(FreshnessValid, AuthorityLookup{AuthorityID: "live_stream:stream-2"})
	select {
	case got := <-asked:
		if got != lookup {
			t.Fatalf("asked for %+v, want %+v", got, lookup)
		}
	case <-time.After(time.Second):
		t.Fatal("the soft-expired authority was not asked for")
	}
	select {
	case got := <-asked:
		t.Fatalf("asked again inside the cooldown, or for a valid authority: %+v", got)
	case <-time.After(100 * time.Millisecond):
	}
}

// Use is reported once per authority per day however often it is decided on,
// and a report that fails is not lost.
func TestUseIsReportedOncePerAuthorityPerDay(t *testing.T) {
	now := storeFixtureNow
	store := fetchTestStore(&now)
	var reported [][]AuthorityUse
	fail := true
	report := func(_ context.Context, uses []AuthorityUse) error {
		if fail {
			return errors.New("commodore down")
		}
		reported = append(reported, uses)
		return nil
	}
	for range 100 {
		store.NoteUse("media_object", "live_stream:stream-1", "tenant-1")
	}
	if err := store.flushUses(context.Background(), report); err == nil {
		t.Fatal("a failed report was treated as sent")
	}
	fail = false
	if err := store.flushUses(context.Background(), report); err != nil {
		t.Fatal(err)
	}
	if len(reported) != 1 || len(reported[0]) != 1 {
		t.Fatalf("a hundred decisions and one failed report sent %v, want one use once", reported)
	}
	store.NoteUse("media_object", "live_stream:stream-1", "tenant-1")
	if err := store.flushUses(context.Background(), report); err != nil || len(reported) != 1 {
		t.Fatalf("the same authority was reported again the same day: %v (err %v)", reported, err)
	}
	now = now.Add(useReportEvery + time.Minute)
	store.NoteUse("media_object", "live_stream:stream-1", "tenant-1")
	if err := store.flushUses(context.Background(), report); err != nil || len(reported) != 2 {
		t.Fatalf("use on a later day was not reported: %v (err %v)", reported, err)
	}
}
