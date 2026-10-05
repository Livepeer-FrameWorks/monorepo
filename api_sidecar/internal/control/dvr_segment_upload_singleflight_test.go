package control

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// Foghorn's finalize loop and the periodic reconciliation can both ask for the
// same segment while a slow PUT of it is still running. Only one PUT of a local
// segment file may be in flight; a duplicate caller is told the upload is
// already owned instead of opening another connection to the object store.
func TestUploadSegmentToS3_SingleFlightPerSegmentFile(t *testing.T) {
	segPath := filepath.Join(t.TempDir(), "53_24.ts")
	if err := os.WriteFile(segPath, []byte("segment-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}

	release := make(chan struct{})
	var active, maxActive, total int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&total, 1)
		n := atomic.AddInt32(&active, 1)
		for {
			m := atomic.LoadInt32(&maxActive)
			if n <= m || atomic.CompareAndSwapInt32(&maxActive, m, n) {
				break
			}
		}
		<-release
		atomic.AddInt32(&active, -1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dm := &DVRManager{logger: logging.NewLogger(), jobs: map[string]*DVRJob{}}

	const callers = 5
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			errs[i] = dm.UploadSegmentForRetry(ctx, segPath, srv.URL+"/bucket/53_24.ts")
		}(i)
	}

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&total) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(200 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&maxActive); got != 1 {
		t.Fatalf("concurrent PUTs of one segment = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&total); got != 1 {
		t.Fatalf("PUTs of one segment = %d, want 1", got)
	}
	var ok, inFlight int
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrSegmentUploadInFlight):
			inFlight++
		default:
			t.Fatalf("unexpected upload error: %v", err)
		}
	}
	if ok != 1 || inFlight != callers-1 {
		t.Fatalf("results: ok=%d in_flight=%d, want 1 and %d", ok, inFlight, callers-1)
	}

	// Once the owner finished, the next attempt uploads again.
	if err := dm.UploadSegmentForRetry(context.Background(), segPath, srv.URL+"/bucket/53_24.ts"); err != nil {
		t.Fatalf("upload after owner finished: %v", err)
	}
}
