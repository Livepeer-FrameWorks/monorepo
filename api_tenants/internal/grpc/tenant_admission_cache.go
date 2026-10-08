package grpc

import (
	"context"
	"errors"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// tenantAdmissionFreshFor is how long one Purser admission answer serves
	// every ValidateTenant for that tenant, and so the longest a suspension or
	// other billing change takes to reach Bridge's admission.
	tenantAdmissionFreshFor = 5 * time.Second
	// tenantAdmissionLastGoodFor is how long a successful answer may stand in
	// for Purser while Purser is failing transiently.
	tenantAdmissionLastGoodFor = 5 * time.Minute
	// tenantAdmissionMaxTenants bounds each cache; eviction only costs a
	// Purser call.
	tenantAdmissionMaxTenants = 10000
)

type tenantAdmissionFetch func(ctx context.Context, tenantID string) (*purserpb.GetTenantAdmissionStatusResponse, error)

// tenantAdmissionCache shares Purser admission answers between ValidateTenant
// calls: concurrent callers for one tenant wait on a single Purser call, an
// answer is reused for freshFor, and a transient Purser failure is answered
// with the tenant's last successful status instead of none.
type tenantAdmissionCache struct {
	fetch       tenantAdmissionFetch
	fresh       *cache.Cache
	lastGood    *cache.Cache
	lastGoodFor time.Duration
}

func newTenantAdmissionCache(fetch tenantAdmissionFetch, freshFor, lastGoodFor time.Duration) *tenantAdmissionCache {
	return &tenantAdmissionCache{
		fetch:       fetch,
		fresh:       cache.New(cache.Options{TTL: freshFor, MaxEntries: tenantAdmissionMaxTenants}, cache.MetricsHooks{}),
		lastGood:    cache.New(cache.Options{TTL: lastGoodFor, MaxEntries: tenantAdmissionMaxTenants}, cache.MetricsHooks{}),
		lastGoodFor: lastGoodFor,
	}
}

func (c *tenantAdmissionCache) status(ctx context.Context, tenantID string) (*purserpb.GetTenantAdmissionStatusResponse, error) {
	value, ok, err := c.fresh.Get(ctx, tenantID, func(loadCtx context.Context, key string) (interface{}, bool, error) {
		resp, fetchErr := c.fetch(loadCtx, key)
		if fetchErr != nil {
			return nil, false, fetchErr
		}
		c.lastGood.Set(key, resp, c.lastGoodFor)
		return resp, true, nil
	})
	if ok {
		if resp, typed := value.(*purserpb.GetTenantAdmissionStatusResponse); typed {
			return resp, nil
		}
	}
	if err == nil {
		return nil, status.Error(codes.Internal, "tenant admission status missing from cache")
	}
	if !transientPurserError(err) {
		// Purser answered about this tenant; an older answer must not outlive it.
		c.lastGood.Delete(tenantID)
		return nil, err
	}
	if previous, found := c.lastGood.Peek(tenantID); found {
		if resp, typed := previous.(*purserpb.GetTenantAdmissionStatusResponse); typed {
			return resp, nil
		}
	}
	return nil, err
}

// transientPurserError reports whether err says Purser could not answer, as
// opposed to an answer about the tenant or the request.
func transientPurserError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted,
		codes.Internal, codes.Unknown, codes.Canceled:
		return true
	default:
		return false
	}
}
