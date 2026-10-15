package control

import (
	"context"
	"errors"
	"sync"
	"time"

	"frameworks/api_balancing/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// thumbnailObjectStorePolicy bounds every object-store request of thumbnail publication. Thumbnail objects are
// small (a poster, a sprite sheet and a VTT manifest), so a HEAD or CopyObject the store has not answered within
// its attempt timeout is stalled rather than slow; abandoning it keeps one stalled request from spending the whole
// completion deadline (thumbnailCompletionDeadline) or a recovery item's budget.
//
// The store keeps executing a copy after the client abandons it and applies writes to one key in order, so a copy
// retried onto the same destination waits behind the abandoned one and stalls with it. Copies therefore never
// retry onto the key they abandoned: a candidate promotion retries onto a fresh per-try key (thumbnailCandidateKey),
// and a deterministic projection copy, whose served key is fixed, is not retried after a timeout and is admitted
// one at a time per key (deterministicCopies).
type thumbnailObjectStorePolicy struct {
	headPolicy          storage.S3AttemptPolicy
	candidatePolicy     storage.S3AttemptPolicy
	deterministicPolicy storage.S3AttemptPolicy
}

var thumbnailObjectStore = thumbnailObjectStorePolicy{
	headPolicy:          storage.S3AttemptPolicy{MaxAttempts: 3, AttemptTimeout: 5 * time.Second, Backoff: 200 * time.Millisecond},
	candidatePolicy:     storage.S3AttemptPolicy{MaxAttempts: thumbnailCandidateTries, AttemptTimeout: 10 * time.Second, Backoff: 200 * time.Millisecond},
	deterministicPolicy: storage.S3AttemptPolicy{MaxAttempts: 3, AttemptTimeout: 30 * time.Second, Backoff: 200 * time.Millisecond, NoRetryAfterTimeout: true},
}

// headThumbnailObject HEADs one thumbnail object under the bounded, retried head policy. A missing object is
// (false, 0, "", nil), as from HeadObjectInfo.
func headThumbnailObject(ctx context.Context, client S3ClientInterface, key string, logger logging.Logger) (exists bool, size int64, etag string, err error) {
	err = storage.RetryS3(ctx, thumbnailObjectStore.headPolicy, func(attemptCtx context.Context) error {
		var hErr error
		exists, size, etag, hErr = client.HeadObjectInfo(attemptCtx, key)
		return hErr
	}, logThumbnailObjectStoreRetry(logger, "HeadObject", key))
	return exists, size, etag, err
}

// promoteThumbnailCandidate copies srcKey, conditional on ifMatchETag, to the completion's candidate key for file.
// Each try writes its own candidate key, so a retry never waits behind the copy it abandoned. It returns the key
// that holds the copy and the destination ETag the provider reported ("" when it omitted it).
func promoteThumbnailCandidate(ctx context.Context, client S3ClientInterface, srcKey, assetKey, token, file, ifMatchETag string, logger logging.Logger) (string, string, error) {
	policy := thumbnailObjectStore.candidatePolicy
	if policy.MaxAttempts <= 0 || policy.MaxAttempts > thumbnailCandidateTries {
		policy.MaxAttempts = thumbnailCandidateTries
	}
	var dstKey, dstETag string
	try := 0
	err := storage.RetryS3(ctx, policy, func(attemptCtx context.Context) error {
		try++
		dstKey = thumbnailCandidateKey(assetKey, token, file, try)
		var pErr error
		dstETag, pErr = client.PromoteObject(attemptCtx, srcKey, dstKey, ifMatchETag)
		return pErr
	}, func(f storage.S3AttemptFailure) {
		logThumbnailObjectStoreRetry(logger, "CopyObject", dstKey)(f)
	})
	return dstKey, dstETag, err
}

// promoteThumbnailDeterministic copies srcKey, conditional on ifMatchETag, to the fixed served key dstKey. The caller
// holds dstKey's admission from deterministicCopies. A timed-out copy is not retried: the retry would write the same
// key and wait behind the abandoned copy.
func promoteThumbnailDeterministic(ctx context.Context, client S3ClientInterface, srcKey, dstKey, ifMatchETag string, logger logging.Logger) (string, error) {
	var dstETag string
	err := storage.RetryS3(ctx, thumbnailObjectStore.deterministicPolicy, func(attemptCtx context.Context) error {
		var pErr error
		dstETag, pErr = client.PromoteObject(attemptCtx, srcKey, dstKey, ifMatchETag)
		return pErr
	}, logThumbnailObjectStoreRetry(logger, "CopyObject", dstKey))
	return dstETag, err
}

func logThumbnailObjectStoreRetry(logger logging.Logger, op, key string) func(storage.S3AttemptFailure) {
	return func(f storage.S3AttemptFailure) {
		logger.WithFields(logging.Fields{
			"op":           op,
			"object_key":   key,
			"attempt":      f.Attempt,
			"max_attempts": f.MaxAttempts,
			"elapsed_ms":   f.Elapsed.Milliseconds(),
			"error":        f.Err,
		}).Warn("Thumbnail object-store request failed; retrying")
	}
}

// errThumbnailCopySuperseded reports that a newer copy to the same served key was requested while this one waited
// for its turn, so this one was dropped.
var errThumbnailCopySuperseded = errors.New("a newer copy to the same served thumbnail key superseded this one")

// deterministicCopies admits this process's copies to each served thumbnail key one at a time. A live stream
// publishes every few seconds, so without it the copies of successive publications (and their retries) pile onto
// one key at the store, which applies them in order; once one stalls, every later copy to that key stalls behind
// it. While a copy runs, only the newest waiting request is kept: an older waiter is released with
// errThumbnailCopySuperseded, since the newer request carries the newer publication.
var deterministicCopies = newLatestKeyGate()

type latestKeyGate struct {
	mu   sync.Mutex
	keys map[string]*latestKeyState
}

type latestKeyState struct {
	waiter chan bool // the newest waiting request; receives true for its turn, false when superseded
}

func newLatestKeyGate() *latestKeyGate {
	return &latestKeyGate{keys: make(map[string]*latestKeyState)}
}

// acquire waits for key's turn. It returns a release func on success, errThumbnailCopySuperseded when a newer
// request replaced this one while it waited, or the context's error.
func (g *latestKeyGate) acquire(ctx context.Context, key string) (func(), error) {
	g.mu.Lock()
	st, busy := g.keys[key]
	if !busy {
		g.keys[key] = &latestKeyState{}
		g.mu.Unlock()
		return func() { g.release(key) }, nil
	}
	if st.waiter != nil {
		st.waiter <- false
	}
	turn := make(chan bool, 1)
	st.waiter = turn
	g.mu.Unlock()

	select {
	case ok := <-turn:
		if !ok {
			return nil, errThumbnailCopySuperseded
		}
		return func() { g.release(key) }, nil
	case <-ctx.Done():
		g.mu.Lock()
		if st.waiter == turn {
			st.waiter = nil
			g.mu.Unlock()
			return nil, ctx.Err()
		}
		g.mu.Unlock()
		// The turn (or a supersession) was sent before this request withdrew; a granted turn is passed on.
		if <-turn {
			g.release(key)
		}
		return nil, ctx.Err()
	}
}

func (g *latestKeyGate) release(key string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	st := g.keys[key]
	if st == nil {
		return
	}
	if st.waiter != nil {
		next := st.waiter
		st.waiter = nil
		next <- true
		return
	}
	delete(g.keys, key)
}
