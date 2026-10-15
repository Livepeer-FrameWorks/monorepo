package control

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/storage"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	smithyhttp "github.com/aws/smithy-go/transport/http"
)

// s3StatusError is an object-store failure carrying an HTTP status, as the SDK returns it.
func s3StatusError(status int, code string) error {
	return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{
		Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
		Err:      errors.New(code),
	}}
}

func shortenThumbnailObjectStore(t *testing.T, attemptTimeout time.Duration) {
	t.Helper()
	prev := thumbnailObjectStore
	thumbnailObjectStore = thumbnailObjectStorePolicy{
		headPolicy:          storage.S3AttemptPolicy{MaxAttempts: 3, AttemptTimeout: attemptTimeout},
		candidatePolicy:     storage.S3AttemptPolicy{MaxAttempts: thumbnailCandidateTries, AttemptTimeout: attemptTimeout},
		deterministicPolicy: storage.S3AttemptPolicy{MaxAttempts: 3, AttemptTimeout: attemptTimeout, NoRetryAfterTimeout: true},
	}
	t.Cleanup(func() { thumbnailObjectStore = prev })
}

// keyOrderedStore models the object store's handling of CopyObject: requests to one destination key are applied in
// arrival order, each taking serviceTime (stallFirst for the first request to a key named in stallKeys), and a request
// keeps executing after its client gives up. It records the most requests ever outstanding on one key and the source
// of the last copy that completed per key.
type keyOrderedStore struct {
	mockS3Client
	serviceTime time.Duration
	stallFirst  time.Duration
	stallKeys   map[string]bool

	smu         sync.Mutex
	tail        map[string]chan struct{}
	outstanding map[string]int
	maxOut      map[string]int
	stalled     map[string]bool
	landed      map[string]string
	issued      map[string]int
}

func newKeyOrderedStore(serviceTime, stallFirst time.Duration, stallKeys ...string) *keyOrderedStore {
	s := &keyOrderedStore{
		serviceTime: serviceTime, stallFirst: stallFirst, stallKeys: map[string]bool{},
		tail: map[string]chan struct{}{}, outstanding: map[string]int{}, maxOut: map[string]int{},
		stalled: map[string]bool{}, landed: map[string]string{}, issued: map[string]int{},
	}
	for _, k := range stallKeys {
		s.stallKeys[k] = true
	}
	return s
}

func (s *keyOrderedStore) PromoteObject(ctx context.Context, src, dst, _ string) (string, error) {
	s.smu.Lock()
	prev := s.tail[dst]
	done := make(chan struct{})
	s.tail[dst] = done
	s.issued[dst]++
	s.outstanding[dst]++
	if s.outstanding[dst] > s.maxOut[dst] {
		s.maxOut[dst] = s.outstanding[dst]
	}
	work := s.serviceTime
	if s.stallKeys[dst] && !s.stalled[dst] {
		s.stalled[dst] = true
		work = s.stallFirst
	}
	s.smu.Unlock()
	go func() {
		if prev != nil {
			<-prev
		}
		time.Sleep(work)
		s.smu.Lock()
		s.outstanding[dst]--
		s.landed[dst] = src
		s.smu.Unlock()
		close(done)
	}()
	select {
	case <-done:
		return "etag-" + src, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (s *keyOrderedStore) stats(key string) (maxOutstanding, issued int, landed string) {
	s.smu.Lock()
	defer s.smu.Unlock()
	return s.maxOut[key], s.issued[key], s.landed[key]
}

// A candidate promotion whose first copy stalls at the store retries onto a fresh key, so the retry does not wait
// behind the abandoned copy (staging: a stalled copy's retries to the same candidate key also stalled in 117 of 164
// chains on one Foghorn).
func TestPromoteThumbnailCandidate_RetryDoesNotQueueBehindTheAbandonedCopy(t *testing.T) {
	shortenThumbnailObjectStore(t, 100*time.Millisecond)
	first := thumbnailCandidateKey("asset-1", "tok", "sprite.jpg", 1)
	store := newKeyOrderedStore(time.Millisecond, 2*time.Second, first)

	key, etag, err := promoteThumbnailCandidate(context.Background(), store, "thumbnails/asset-1/.staging/a/sprite.jpg",
		"asset-1", "tok", "sprite.jpg", "etag-staging", logging.NewLoggerWithService("test"))
	if err != nil {
		t.Fatalf("promotion must land on a fresh key after the stalled try: %v", err)
	}
	if want := thumbnailCandidateKey("asset-1", "tok", "sprite.jpg", 2); key != want || etag == "" {
		t.Fatalf("want the second try's key %q with its ETag, got %q etag=%q", want, key, etag)
	}
	if maxOut, issued, _ := store.stats(first); maxOut != 1 || issued != 1 {
		t.Fatalf("the stalled key received %d copies (%d outstanding at once); a retry must not reuse it", issued, maxOut)
	}
}

// Every try's candidate key is reclaimable: cleanup enumerates the keys a completion may have written, including an
// abandoned try the store finished after the client gave up.
func TestThumbnailCandidateKeys_CoverEveryTry(t *testing.T) {
	keys := thumbnailCandidateKeys("asset-1", "tok", "poster.jpg")
	if len(keys) != thumbnailCandidateTries {
		t.Fatalf("want %d keys, got %v", thumbnailCandidateTries, keys)
	}
	seen := map[string]bool{}
	for try := 1; try <= thumbnailCandidateTries; try++ {
		k := thumbnailCandidateKey("asset-1", "tok", "poster.jpg", try)
		if keys[try-1] != k || seen[k] {
			t.Fatalf("try %d key %q missing or duplicated in %v", try, k, keys)
		}
		seen[k] = true
	}
	if keys[0] != ThumbnailVersionKey("asset-1", "tok", "poster.jpg") {
		t.Fatalf("the first try keeps the token's version key, got %q", keys[0])
	}
}

// A live stream projects every publication to the same served key. When one copy to that key stalls at the store,
// the copies of the following publications and their retries must not pile onto the key: staging saw one served
// sprite.jpg fail 159 times over 50 minutes while the stream kept publishing every 5 s and its other files copied
// fine. Publications here arrive faster than the key's service time allows for three copies each, so a pile-up never
// drains; admitted one at a time with the newest waiting projection kept, the key recovers once the stall clears.
func TestCopyThumbnailObjectsToDeterministic_SuccessivePublicationsDoNotPileOntoAStalledKey(t *testing.T) {
	shortenThumbnailObjectStore(t, 150*time.Millisecond)
	detKey := ThumbnailDeterministicKey("live-1", "sprite.jpg")
	store := newKeyOrderedStore(30*time.Millisecond, 500*time.Millisecond, detKey)
	logger := logging.NewLoggerWithService("test")

	var wg sync.WaitGroup
	var mu sync.Mutex
	var landedAfterStall int
	start := time.Now()
	for i := 0; i < 40; i++ {
		src := ThumbnailVersionKey("live-1", "tok-"+strconv.Itoa(i), "sprite.jpg")
		wg.Add(1)
		go func(src string) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			objs := []ThumbnailObject{{FileName: "sprite.jpg", VersionKey: src, ETag: "etag-version"}}
			if copyThumbnailObjectsToDeterministic(ctx, store, "live-1", objs, logger) && time.Since(start) > time.Second {
				mu.Lock()
				landedAfterStall++
				mu.Unlock()
			}
		}(src)
		time.Sleep(50 * time.Millisecond)
	}
	wg.Wait()

	maxOut, issued, landed := store.stats(detKey)
	if landedAfterStall == 0 {
		t.Fatalf("no publication reached the served key after the stall cleared (%d copies issued, %d outstanding at once)", issued, maxOut)
	}
	// One copy is admitted at a time and none is retried after a timeout, so copies abandoned during the stall
	// accumulate at most once per attempt timeout, independent of how many publications arrive.
	if bound := int(500*time.Millisecond/(150*time.Millisecond)) + 2; maxOut > bound {
		t.Fatalf("%d copies were outstanding on the served key at once; want at most %d", maxOut, bound)
	}
	if landed != ThumbnailVersionKey("live-1", "tok-39", "sprite.jpg") {
		t.Fatalf("the served key must end with the newest publication, got %q", landed)
	}
}

// Every HEAD and CopyObject of the deterministic projection carries its own deadline, well under the 2-minute
// completion deadline, even when the caller passes an unbounded context.
func TestCopyThumbnailObjectsToDeterministic_EveryRequestHasAnAttemptDeadline(t *testing.T) {
	var mu sync.Mutex
	var unbounded []string
	check := func(ctx context.Context, op string) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Second {
			mu.Lock()
			unbounded = append(unbounded, op)
			mu.Unlock()
		}
	}
	m := &mockS3Client{
		headObjectInfoFn: func(ctx context.Context, key string) (bool, int64, string, error) {
			check(ctx, "HEAD "+key)
			return true, 10, "etag", nil
		},
		promoteObjectFn: func(ctx context.Context, src, dst, _ string) error {
			check(ctx, "COPY "+dst)
			return nil
		},
	}
	objs := thumbnailObjectsFromToken("stream-1", "tok", []string{"poster.jpg", "sprite.jpg"})
	if !copyThumbnailObjectsToDeterministic(context.Background(), m, "stream-1", objs, logging.NewLoggerWithService("test")) {
		t.Fatal("expected every file copied")
	}
	if len(unbounded) != 0 {
		t.Fatalf("object-store requests without a per-attempt deadline: %v", unbounded)
	}
}

// A deterministic copy the store does not answer is abandoned at its attempt timeout and left for recovery rather
// than retried onto the same served key, where the retry would wait behind the abandoned copy. An answered transient
// failure (503) is still retried.
func TestCopyThumbnailObjectsToDeterministic_StalledCopyIsNotRetriedOntoTheSameKey(t *testing.T) {
	shortenThumbnailObjectStore(t, 50*time.Millisecond)
	m := &mockS3Client{
		promoteObjectFn: func(ctx context.Context, _, _, _ string) error {
			<-ctx.Done()
			return ctx.Err()
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := time.Now()
	objs := thumbnailObjectsFromToken("stream-1", "tok", []string{"poster.jpg"})
	objs[0].ETag = "etag-version"
	if copyThumbnailObjectsToDeterministic(ctx, m, "stream-1", objs, logging.NewLoggerWithService("test")) {
		t.Fatal("a stalled copy must leave the projection to recovery")
	}
	if len(m.promoteCalls) != 1 {
		t.Fatalf("want exactly one copy to the served key, got %v", m.promoteCalls)
	}
	if time.Since(started) > 2*time.Second {
		t.Fatalf("stalled copy held the projection for %s", time.Since(started))
	}

	unavailable := s3StatusError(http.StatusServiceUnavailable, "SlowDown")
	calls := 0
	m = &mockS3Client{promoteObjectFn: func(context.Context, string, string, string) error {
		calls++
		if calls == 1 {
			return unavailable
		}
		return nil
	}}
	if !copyThumbnailObjectsToDeterministic(ctx, m, "stream-1", objs, logging.NewLoggerWithService("test")) || len(m.promoteCalls) != 2 {
		t.Fatalf("an answered 503 must be retried and land, got %v", m.promoteCalls)
	}
}

// With the source ETag already known, the projection copies without a HEAD; a copy refused on that ETag (412)
// re-reads the source and copies under the fresh ETag.
func TestCopyThumbnailObjectsToDeterministic_KnownETagSkipsTheHead(t *testing.T) {
	logger := logging.NewLoggerWithService("test")
	objs := thumbnailObjectsFromToken("stream-1", "tok", []string{"poster.jpg", "sprite.jpg"})
	for i := range objs {
		objs[i].ETag = "etag-version"
	}

	var used []string
	m := &mockS3Client{promoteObjectFn: func(_ context.Context, _, _, ifMatch string) error {
		used = append(used, ifMatch)
		return nil
	}}
	if !copyThumbnailObjectsToDeterministic(context.Background(), m, "stream-1", objs, logger) {
		t.Fatal("expected every file copied")
	}
	if len(m.headCalls) != 0 || len(used) != 2 || used[0] != "etag-version" || used[1] != "etag-version" {
		t.Fatalf("known ETag must be used with no HEAD: heads=%v ifMatch=%v", m.headCalls, used)
	}

	refused := s3StatusError(http.StatusPreconditionFailed, "PreconditionFailed")
	used = nil
	m = &mockS3Client{promoteObjectFn: func(_ context.Context, _, _, ifMatch string) error {
		used = append(used, ifMatch)
		if ifMatch == "etag-version" {
			return refused
		}
		return nil
	}}
	if !copyThumbnailObjectsToDeterministic(context.Background(), m, "stream-1", objs[:1], logger) {
		t.Fatal("a copy refused on a stale ETag must fall back to the HEAD and land")
	}
	if len(m.headCalls) != 1 || len(used) != 2 || used[1] != "etag-mock" {
		t.Fatalf("want one fallback HEAD then a copy under its ETag: heads=%v ifMatch=%v", m.headCalls, used)
	}
}
