package federation

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// AdmissionCallTimeout bounds one central-service call on a publisher or
	// viewer admission path, leaving the rest of the admission budget for the
	// signed local authority and the cell's own database.
	AdmissionCallTimeout = time.Second
	// centralOutageBackoff matches the authority fetcher's outage backoff:
	// once a central service is known unreachable, admission answers from
	// local state without waiting on it again.
	centralOutageBackoff = 5 * time.Second
	// A slow answer for one request is not an outage; consecutive timeouts
	// with no answer in between are.
	centralTimeoutsToBackOff = 3
)

// CentralOutageBackoff is the process-wide record of whether one central
// service (Commodore, Quartermaster) is reachable for admission. Every
// admission call to that service shares one instance, so one detected outage
// spares every following admission the wait.
type CentralOutageBackoff struct {
	// Service names the central service in the error a skipped call returns.
	Service string
	// Now is the backoff clock; nil uses the wall clock.
	Now func() time.Time

	mu          sync.Mutex
	outageUntil time.Time
	timeouts    int
}

func (backoff *CentralOutageBackoff) now() time.Time {
	if backoff.Now != nil {
		return backoff.Now()
	}
	return time.Now()
}

func (backoff *CentralOutageBackoff) service() string {
	if service := strings.TrimSpace(backoff.Service); service != "" {
		return service
	}
	return "central service"
}

// InOutage reports whether the service was found unreachable within the backoff.
func (backoff *CentralOutageBackoff) InOutage() bool {
	backoff.mu.Lock()
	defer backoff.mu.Unlock()
	return backoff.now().Before(backoff.outageUntil)
}

// RecordAnswer notes that the service answered.
func (backoff *CentralOutageBackoff) RecordAnswer() {
	backoff.mu.Lock()
	backoff.timeouts = 0
	backoff.mu.Unlock()
}

// RecordUnreachable reports whether err means the service could not answer,
// and opens the backoff when that is established: at once for Unavailable
// (including an open circuit breaker), after consecutive timeouts otherwise.
func (backoff *CentralOutageBackoff) RecordUnreachable(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable:
		backoff.mu.Lock()
		backoff.outageUntil = backoff.now().Add(centralOutageBackoff)
		backoff.mu.Unlock()
		return true
	case codes.DeadlineExceeded:
		backoff.mu.Lock()
		backoff.timeouts++
		if backoff.timeouts >= centralTimeoutsToBackOff {
			backoff.outageUntil = backoff.now().Add(centralOutageBackoff)
		}
		backoff.mu.Unlock()
		return true
	default:
		return false
	}
}

// CentralUnreachable reports whether err from Call means the service could
// not answer, as opposed to an answer that refuses.
func CentralUnreachable(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded:
		return true
	default:
		return false
	}
}

// Call runs one central-service call on an admission path: a single attempt
// bounded to AdmissionCallTimeout that does not wait for the connection to
// become ready, skipped with Unavailable while the backoff is open. A timeout
// of the call itself is reported as DeadlineExceeded; the caller's own budget
// ending is returned as the context error and says nothing about the service.
func (backoff *CentralOutageBackoff) Call(ctx context.Context, call func(context.Context) error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return status.FromContextError(contextErr).Err()
	}
	if backoff.InOutage() {
		return status.Errorf(codes.Unavailable, "%s is in outage backoff", backoff.service())
	}
	callCtx, cancel := context.WithTimeout(clients.WithSingleAttempt(ctx), AdmissionCallTimeout)
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
