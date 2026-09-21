package mediaauthority

import (
	"context"
	"errors"
	"sync"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"

	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// A decision waits this long for an authority the cell does not hold. It is
	// spent inside a blocking trigger, whose whole budget is four seconds.
	authorityFetchTimeout = 2 * time.Second
	// How long "no such object" is remembered, so a scanner guessing names costs
	// one request per name per window.
	authorityFetchNotFoundTTL = 30 * time.Second
	// After a fetch fails for any other reason, decisions stop waiting for a
	// while. Without this every decision about an object the cell does not hold
	// would wait out the timeout for as long as the control plane is unreachable.
	authorityFetchOutageBackoff = 5 * time.Second
	// A soft-expired authority is asked for again at most this often.
	authorityFetchRefreshCooldown    = time.Minute
	authorityFetchMemoryLimit        = 20_000
	authorityFetchAsyncLimit         = 8
	authorityFetchUnsupportedBackoff = 30 * time.Second
)

// ErrAuthorityFetchUnavailable reports that a fetch could not be tried: no
// fetcher is installed, the control plane predates fetching, or a recent fetch
// failed. The caller carries on as it would have without fetching.
var ErrAuthorityFetchUnavailable = errors.New("media authority fetch is unavailable")

// AuthorityLookup names the authority a decision needs. Exactly one field is set.
// TenantID asks for the tenant authority alone.
type AuthorityLookup struct {
	AuthorityID  string
	PlaybackID   string
	InternalName string
	TenantID     string
}

func (l AuthorityLookup) key() string {
	switch {
	case l.AuthorityID != "":
		return "a\x00" + l.AuthorityID
	case l.PlaybackID != "":
		return "p\x00" + l.PlaybackID
	case l.InternalName != "":
		return "n\x00" + l.InternalName
	default:
		return "t\x00" + l.TenantID
	}
}

func (l AuthorityLookup) empty() bool {
	return l.AuthorityID == "" && l.PlaybackID == "" && l.InternalName == "" && l.TenantID == ""
}

// AuthorityFetcher asks the control plane for current authority and returns
// the encoded envelopes signed for this cell.
type AuthorityFetcher func(ctx context.Context, lookup AuthorityLookup) ([][]byte, error)

type fetchCoordinator struct {
	mu          sync.Mutex
	fetch       AuthorityFetcher
	outageUntil time.Time
	remembered  map[string]time.Time
	group       singleflight.Group
	asyncActive int
}

// SetAuthorityFetcher installs how this cell asks for an authority it does not
// hold. Installing nil leaves every decision on what the cell already holds.
func (s *Store) SetAuthorityFetcher(fetch AuthorityFetcher) {
	if s == nil || s.fetcher == nil {
		return
	}
	s.fetcher.mu.Lock()
	s.fetcher.fetch = fetch
	s.fetcher.mu.Unlock()
}

// Fetch asks the control plane for an authority this cell does not hold, or
// holds past its validity, and applies what comes back. The cell holds only
// what is in use; asking is what brings an object back, and it is also how the
// control plane learns the object is in use again. applied reports that local
// state advanced, so the caller should read again.
//
// Nothing is ever removed here. A failed or refused fetch leaves the cell
// deciding on exactly what it held before.
//
// Every decision that asks is counted here, whichever path it came from
// (triggers, placement and federation, the playback API, background refresh),
// once per decision: callers sharing one fetch each count what they got.
func (s *Store) Fetch(ctx context.Context, lookup AuthorityLookup) (applied bool, err error) {
	applied, err = s.fetch(ctx, lookup)
	if s != nil && s.fetchOutcomes != nil {
		s.fetchOutcomes.WithLabelValues(fetchOutcome(applied, err)).Inc()
	}
	return applied, err
}

// FetchOutcomes are the outcome labels of media_authority_fetches_total.
// not_installed is a cell with no control-plane fetcher configured. An
// unsupported RPC is an unavailable fetch and is retried after backoff.
var FetchOutcomes = []string{"applied", "nothing", "unavailable", "not_installed"}

func fetchOutcome(applied bool, err error) string {
	switch {
	case errors.Is(err, errAuthorityFetchNotInstalled):
		return "not_installed"
	case errors.Is(err, errAuthorityFetchRemembered), status.Code(err) == codes.NotFound:
		return "nothing"
	case err != nil:
		return "unavailable"
	case applied:
		return "applied"
	default:
		return "nothing"
	}
}

// SetFetchOutcomeMetric installs the counter every fetch is recorded on.
func (s *Store) SetFetchOutcomeMetric(outcomes *prometheus.CounterVec) {
	if s != nil {
		s.fetchOutcomes = outcomes
	}
}

var (
	errAuthorityFetchNotInstalled = errors.Join(ErrAuthorityFetchUnavailable, errors.New("no media authority fetcher is installed"))
	// A fetch that recently found nothing for this cell is not asked again yet.
	errAuthorityFetchRemembered = errors.Join(ErrAuthorityFetchUnavailable, errors.New("nothing to fetch for this authority, recently asked"))
)

func (s *Store) fetch(ctx context.Context, lookup AuthorityLookup) (applied bool, err error) {
	if s == nil || s.fetcher == nil || lookup.empty() {
		return false, errAuthorityFetchNotInstalled
	}
	key, now := lookup.key(), s.now().UTC()
	s.fetcher.mu.Lock()
	fetch := s.fetcher.fetch
	outage, remembered := now.Before(s.fetcher.outageUntil), now.Before(s.fetcher.remembered[key])
	s.fetcher.mu.Unlock()
	switch {
	case fetch == nil:
		return false, errAuthorityFetchNotInstalled
	case outage:
		return false, ErrAuthorityFetchUnavailable
	case remembered:
		return false, errAuthorityFetchRemembered
	}
	shared := s.fetcher.group.DoChan(key, func() (any, error) {
		// The fetch belongs to every caller waiting on it, not to the first one.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), authorityFetchTimeout)
		defer cancel()
		generation := s.fence.confirmationGeneration()
		startedAt := s.now().UTC()
		if s.db != nil {
			var clockErr error
			startedAt, clockErr = foghorndb.New(s.db).BeginMediaAuthorityConfirmation(fetchCtx)
			if clockErr != nil {
				return false, clockErr
			}
		}
		envelopes, fetchErr := fetch(fetchCtx, lookup)
		if fetchErr != nil {
			s.rememberFetchFailure(key, fetchErr)
			return false, fetchErr
		}
		advanced := false
		for _, encoded := range envelopes {
			applyResult, applyErr := s.apply(fetchCtx, encoded, startedAt, generation)
			if applyErr != nil {
				// A version the cell already passed is not a failure to decide on.
				if errors.Is(applyErr, ErrRollback) {
					continue
				}
				return advanced, applyErr
			}
			// A current fetch can confirm an already-held version withheld by a
			// trust barrier. The reread checks whether its start cleared that barrier.
			advanced = advanced || applyResult.Status == ApplyStatusApplied || applyResult.Status == ApplyStatusDuplicate
		}
		if !advanced {
			// Nothing for this cell: the tenant holds no grant that names it.
			// Asking again at once would say the same.
			s.rememberFetch(key, authorityFetchNotFoundTTL)
		}
		return advanced, nil
	})
	// A caller whose own deadline ends first stops waiting; the fetch carries on
	// for the others waiting on it, and what it applies is there for the next read.
	select {
	case <-ctx.Done():
		return false, errors.Join(ErrAuthorityFetchUnavailable, ctx.Err())
	case outcome := <-shared:
		advanced, _ := outcome.Val.(bool) //nolint:errcheck // the group only ever returns a bool
		if outcome.Err != nil {
			return advanced, errors.Join(ErrAuthorityFetchUnavailable, outcome.Err)
		}
		return advanced, nil
	}
}

// refreshAsync asks for a fresh version of an authority this cell still holds
// as valid but past refresh_after. Renewal reaches a cell well before that, so
// getting here means it is overdue; the read that noticed goes ahead on what
// the cell holds.
func (s *Store) refreshAsync(lookup AuthorityLookup) bool {
	if s == nil || s.fetcher == nil || lookup.empty() {
		return false
	}
	key, now := "refresh\x00"+lookup.key(), s.now().UTC()
	s.fetcher.mu.Lock()
	installed := s.fetcher.fetch != nil
	due := installed && s.fetcher.asyncActive < authorityFetchAsyncLimit && !now.Before(s.fetcher.remembered[key]) && !now.Before(s.fetcher.outageUntil)
	if due {
		s.fetcher.asyncActive++
		s.rememberFetchLocked(key, authorityFetchRefreshCooldown, now)
	}
	s.fetcher.mu.Unlock()
	if due {
		go func() {
			defer func() {
				s.fetcher.mu.Lock()
				s.fetcher.asyncActive--
				s.fetcher.mu.Unlock()
			}()
			_, _ = s.Fetch(context.Background(), lookup) //nolint:errcheck // advisory: the authority the cell holds stays usable
		}()
	}
	return installed
}

// RefreshAuthorityAsync requests a current pair without blocking an apply
// observer on a fetch that itself invokes that observer after commit.
func (s *Store) RefreshAuthorityAsync(lookup AuthorityLookup) {
	s.refreshAsync(lookup)
}

func (s *Store) rememberFetchFailure(key string, err error) {
	switch status.Code(err) {
	case codes.NotFound:
		s.rememberFetch(key, authorityFetchNotFoundTTL)
	case codes.Aborted, codes.FailedPrecondition, codes.InvalidArgument, codes.PermissionDenied:
		// Object contention, invalid requests and explicit refusals say nothing
		// about whether unrelated authority can be fetched from the control plane.
		return
	case codes.Unimplemented:
		// gRPC reconnects without reinstalling the fetcher. Retry at a bounded
		// rate so an upgraded core repairs this cell without a cell restart.
		s.fetcher.mu.Lock()
		s.fetcher.outageUntil = s.now().UTC().Add(authorityFetchUnsupportedBackoff)
		s.fetcher.mu.Unlock()
	default:
		s.fetcher.mu.Lock()
		s.fetcher.outageUntil = s.now().UTC().Add(authorityFetchOutageBackoff)
		s.fetcher.mu.Unlock()
	}
}

func (s *Store) rememberFetch(key string, ttl time.Duration) {
	s.fetcher.mu.Lock()
	s.rememberFetchLocked(key, ttl, s.now().UTC())
	s.fetcher.mu.Unlock()
}

func (s *Store) rememberFetchLocked(key string, ttl time.Duration, now time.Time) {
	if s.fetcher.remembered == nil {
		s.fetcher.remembered = map[string]time.Time{}
	}
	if len(s.fetcher.remembered) >= authorityFetchMemoryLimit {
		for remembered, until := range s.fetcher.remembered {
			if !now.Before(until) {
				delete(s.fetcher.remembered, remembered)
			}
		}
		// Still full of live entries: forgetting them costs repeated requests,
		// never a wrong decision.
		if len(s.fetcher.remembered) >= authorityFetchMemoryLimit {
			s.fetcher.remembered = map[string]time.Time{}
		}
	}
	s.fetcher.remembered[key] = now.Add(ttl)
}
