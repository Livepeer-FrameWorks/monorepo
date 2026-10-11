package federation

import (
	"context"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// CommodoreAdmissionCallTimeout bounds one Commodore call on a publisher
	// admission path, leaving the rest of the admission budget for the signed
	// outage owner path and the cell's own database.
	CommodoreAdmissionCallTimeout = time.Second
	// commodoreOutageBackoff matches the authority fetcher's outage backoff:
	// once Commodore is known unreachable, admission answers from signed local
	// state without waiting on it again.
	commodoreOutageBackoff = 5 * time.Second
	// A slow answer for one stream is not an outage; consecutive timeouts with
	// no answer in between are.
	commodoreTimeoutsToBackOff = 3
)

// CommodoreOutageBackoff is the process-wide record of whether Commodore is
// reachable for publisher admission. The ingest-ownership read and the
// PUSH_REWRITE validation and claim calls share one instance, so one detected
// outage spares every following admission the wait.
type CommodoreOutageBackoff struct {
	// Now is the backoff clock; nil uses the wall clock.
	Now func() time.Time

	mu          sync.Mutex
	outageUntil time.Time
	timeouts    int
}

func (backoff *CommodoreOutageBackoff) now() time.Time {
	if backoff.Now != nil {
		return backoff.Now()
	}
	return time.Now()
}

// InOutage reports whether Commodore was found unreachable within the backoff.
func (backoff *CommodoreOutageBackoff) InOutage() bool {
	backoff.mu.Lock()
	defer backoff.mu.Unlock()
	return backoff.now().Before(backoff.outageUntil)
}

// RecordAnswer notes that Commodore answered.
func (backoff *CommodoreOutageBackoff) RecordAnswer() {
	backoff.mu.Lock()
	backoff.timeouts = 0
	backoff.mu.Unlock()
}

// RecordUnreachable reports whether err means Commodore could not answer, and
// opens the backoff when that is established: at once for Unavailable
// (including an open circuit breaker), after consecutive timeouts otherwise.
func (backoff *CommodoreOutageBackoff) RecordUnreachable(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable:
		backoff.mu.Lock()
		backoff.outageUntil = backoff.now().Add(commodoreOutageBackoff)
		backoff.mu.Unlock()
		return true
	case codes.DeadlineExceeded:
		backoff.mu.Lock()
		backoff.timeouts++
		if backoff.timeouts >= commodoreTimeoutsToBackOff {
			backoff.outageUntil = backoff.now().Add(commodoreOutageBackoff)
		}
		backoff.mu.Unlock()
		return true
	default:
		return false
	}
}

// CommodoreUnreachable reports whether err from Call means Commodore could not
// answer, as opposed to an answer that refuses.
func CommodoreUnreachable(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	default:
		return false
	}
}

// Call runs one Commodore call on a publisher admission path: a single
// attempt bounded to CommodoreAdmissionCallTimeout that does not wait for the
// connection to become ready, skipped with Unavailable while the backoff is
// open. A timeout of the call itself is reported as DeadlineExceeded; the
// caller's own budget ending is returned as the context error and says
// nothing about Commodore.
func (backoff *CommodoreOutageBackoff) Call(ctx context.Context, call func(context.Context) error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return status.FromContextError(contextErr).Err()
	}
	if backoff.InOutage() {
		return status.Error(codes.Unavailable, "commodore is in outage backoff")
	}
	callCtx, cancel := context.WithTimeout(clients.WithSingleAttempt(ctx), CommodoreAdmissionCallTimeout)
	defer cancel()
	err := call(callCtx)
	if contextErr := ctx.Err(); contextErr != nil {
		return status.FromContextError(contextErr).Err()
	}
	if err == nil {
		backoff.RecordAnswer()
		return nil
	}
	if callCtx.Err() != nil {
		err = status.FromContextError(callCtx.Err()).Err()
	}
	backoff.RecordUnreachable(err)
	return err
}
