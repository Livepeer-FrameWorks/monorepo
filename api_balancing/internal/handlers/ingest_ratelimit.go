package handlers

import (
	"container/list"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	sharedmw "github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"

	"github.com/gin-gonic/gin"
)

// The ingest front door is unauthenticated and its Commodore lookup is
// deliberately uncached, so a stream-key guess costs a database round trip.
// A per-IP bucket keeps enumeration from turning into a Commodore/Postgres
// amplifier; it is admission control for the resolver, not billing policy.
type ingestBucket struct {
	ip       string
	tokens   float64
	lastFill time.Time
}

type ingestRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*list.Element
	lru     *list.List // front = most recently used

	ratePerSec float64
	burst      float64
	maxBuckets int

	// Raw env values the current settings were derived from, so a SIGHUP
	// env reload is actually picked up: Foghorn advertises reload, and
	// settings frozen at construction would make it silently untrue.
	rawRate    string
	rawBurst   string
	rawBuckets string
}

var ingestLimiter = newIngestRateLimiter()

func ingestLimiterEnv() (rawRate, rawBurst, rawBuckets string) {
	return config.GetEnv("INGEST_RESOLVE_RATE_PER_MIN", ""),
		config.GetEnv("INGEST_RESOLVE_BURST", ""),
		config.GetEnv("INGEST_RESOLVE_MAX_BUCKETS", "")
}

func newIngestRateLimiter() *ingestRateLimiter {
	l := &ingestRateLimiter{
		buckets: make(map[string]*list.Element),
		lru:     list.New(),
	}
	l.applySettings()
	return l
}

// applySettings re-reads the limits from the environment. Buckets are kept:
// only the refill rate and ceilings change, so a reload does not hand every
// caller a fresh allowance.
func (l *ingestRateLimiter) applySettings() {
	rawRate, rawBurst, rawBuckets := ingestLimiterEnv()
	l.ratePerSec = float64(max(config.GetEnvInt("INGEST_RESOLVE_RATE_PER_MIN", 60), 1)) / 60.0
	l.burst = float64(max(config.GetEnvInt("INGEST_RESOLVE_BURST", 10), 1))
	l.maxBuckets = max(config.GetEnvInt("INGEST_RESOLVE_MAX_BUCKETS", 50000), 1)
	l.rawRate, l.rawBurst, l.rawBuckets = rawRate, rawBurst, rawBuckets
}

// refreshSettingsLocked reapplies limits when the environment has changed
// underneath a running process (SIGHUP env-file reload), reporting whether
// anything changed.
func (l *ingestRateLimiter) refreshSettingsLocked() bool {
	rawRate, rawBurst, rawBuckets := ingestLimiterEnv()
	if rawRate == l.rawRate && rawBurst == l.rawBurst && rawBuckets == l.rawBuckets {
		return false
	}
	l.applySettings()
	// Lowering the burst is a ceiling change, not a promise that existing
	// callers keep their old allowance. Clamp live buckets immediately so a
	// reload cannot leave them above the configured maximum.
	for elem := l.lru.Front(); elem != nil; elem = elem.Next() {
		if bucket := bucketOf(elem); bucket != nil {
			bucket.tokens = min(bucket.tokens, l.burst)
		}
	}
	return true
}

// shrinkToCapLocked evicts least-recently-used buckets until the map is at or
// below the configured ceiling.
func (l *ingestRateLimiter) shrinkToCapLocked() { l.evictDownTo(l.maxBuckets) }

// reserveSlotLocked frees one slot so a new bucket can be added without
// exceeding the ceiling.
func (l *ingestRateLimiter) reserveSlotLocked() { l.evictDownTo(l.maxBuckets - 1) }

func (l *ingestRateLimiter) evictDownTo(target int) {
	for len(l.buckets) > target {
		oldest := l.lru.Back()
		if oldest == nil {
			return
		}
		l.lru.Remove(oldest)
		if evicted := bucketOf(oldest); evicted != nil {
			delete(l.buckets, evicted.ip)
		}
	}
}

// allow consumes a token for ip, returning false when the bucket is empty
// along with how long the caller should wait. Steady-state work is O(1) per
// call — LRU touch plus a single eviction at the cap — so a flood of distinct
// addresses cannot degrade into a scan. The one exception is the first call
// after a reload that lowers the cap, which evicts down to the new ceiling.
func (l *ingestRateLimiter) allow(ip string, now time.Time) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.refreshSettingsLocked() {
		// A reload can lower the cap below the current size; shrink to the new
		// ceiling now rather than waiting for a request from an unseen address,
		// which may never come. Shrinking stops AT the cap — evicting while
		// already at it would drop a live bucket (possibly this caller's, which
		// would then be recreated with a fresh burst).
		l.shrinkToCapLocked()
	}

	elem, ok := l.buckets[ip]
	if !ok {
		// Reserving room for a new bucket needs one free slot, so this one
		// evicts down to cap-1.
		l.reserveSlotLocked()
		elem = l.lru.PushFront(&ingestBucket{ip: ip, tokens: l.burst - 1, lastFill: now})
		l.buckets[ip] = elem
		return true, 0
	}

	l.lru.MoveToFront(elem)
	b := bucketOf(elem)
	if b == nil {
		return true, 0
	}
	if elapsed := now.Sub(b.lastFill).Seconds(); elapsed > 0 {
		b.tokens = min(l.burst, b.tokens+elapsed*l.ratePerSec)
		b.lastFill = now
	}
	if b.tokens < 1 {
		deficit := (1 - b.tokens) / l.ratePerSec
		return false, time.Duration(deficit * float64(time.Second))
	}
	b.tokens--
	return true, 0
}

// evictIdle drops buckets that have refilled to full and stayed untouched, so
// that forgetting them changes no decision. A bucket is only droppable once
// enough time has passed to refill the whole burst — with a slow rate and a
// large burst that is much longer than the sweep interval, so idleFor alone is
// not a safe proxy.
func (l *ingestRateLimiter) evictIdle(now time.Time, idleFor time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	refillSeconds := l.burst / l.ratePerSec
	minIdle := max(time.Duration(refillSeconds*float64(time.Second)), idleFor)

	for elem := l.lru.Back(); elem != nil; {
		prev := elem.Prev()
		b := bucketOf(elem)
		if b != nil && now.Sub(b.lastFill) < minIdle {
			break // LRU order means everything ahead is fresher
		}
		l.lru.Remove(elem)
		if b != nil {
			delete(l.buckets, b.ip)
		}
		elem = prev
	}
}

func bucketOf(elem *list.Element) *ingestBucket {
	b, ok := elem.Value.(*ingestBucket)
	if !ok {
		return nil
	}
	return b
}

// startIngestRateLimiterJanitor sweeps idle buckets for the process lifetime.
//
// Guarded rather than documented as call-once: a repeated Init would otherwise
// leak another permanent goroutine. Tests exercise evictIdle directly, so no
// sweeper runs underneath them.
func startIngestRateLimiterJanitor() {
	ingestJanitorOnce.Do(runIngestRateLimiterJanitor)
}

var ingestJanitorOnce sync.Once

func runIngestRateLimiterJanitor() {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for now := range ticker.C {
			ingestLimiter.evictIdle(now, 10*time.Minute)
		}
	}()
}

// Proxy trust comes from the shared env-backed set in pkg/middleware, so it
// re-reads on a SIGHUP env reload and cannot drift from Gateway's view of who
// a caller is.
var trustedProxiesOnce sync.Once
var trustedProxies *sharedmw.TrustedProxies

func currentTrustedProxies() *sharedmw.TrustedProxies {
	trustedProxiesOnce.Do(func() {
		trustedProxies = sharedmw.TrustedProxiesFromEnv(
			"TRUSTED_PROXY_CIDRS",
			func(key string) string { return config.GetEnv(key, "") },
			func(invalid []string) {
				if logger != nil {
					logger.WithField("entries", strings.Join(invalid, ",")).
						Warn("TRUSTED_PROXY_CIDRS: ignoring unparseable entries (want IP or CIDR)")
				}
			},
		)
	})
	return trustedProxies
}

// trustedClientIP returns the client address to attribute a request to.
//
// gin's own ClientIP honours X-Forwarded-For from any peer, because the shared
// service router never configures trusted proxies — that would let a caller
// mint a fresh rate-limit bucket per request just by varying a header.
//
// Forwarding headers are believed only from a proxy named in
// TRUSTED_PROXY_CIDRS; trust is never inferred from address shape, since a VPN
// peer or a neighbouring container also holds a private address. Where Foghorn
// is fronted by a proxy and that variable is unset, every publisher is
// attributed to the proxy: one shared bucket and proxy-located geo.
func trustedClientIP(c *gin.Context) string {
	return sharedmw.TrustedClientIP(c, currentTrustedProxies())
}

// enforceIngestRateLimit writes a 429 and reports false when the caller is over
// budget.
func enforceIngestRateLimit(c *gin.Context, clientIP string) bool {
	allowed, retryAfter := ingestLimiter.allow(clientIP, time.Now())
	if allowed {
		return true
	}
	seconds := max(int(retryAfter.Seconds()), 1)
	c.Header("Retry-After", strconv.Itoa(seconds))
	respondPlaybackError(c, http.StatusTooManyRequests, "RATE_LIMITED",
		"Too many ingest resolution requests", nil)
	return false
}
