package grpc

import (
	"context"
	"sync"

	"golang.org/x/sync/singleflight"
)

// mediaAuthorityCompileMemo shares the inputs a tenant's objects all derive
// from between the compiles of one claim. Its lifetime is its correctness: it is
// created before the claim it serves and dropped with it, so nothing in it was
// read before the rows were enqueued. A cache that outlived a claim could hand a
// compile the state from before the change that enqueued it, and because an
// unchanged compile publishes nothing, that mistake would never be corrected.
type mediaAuthorityCompileMemo struct {
	mu      sync.Mutex
	entries map[string]any
	group   singleflight.Group
}

func newMediaAuthorityCompileMemo() *mediaAuthorityCompileMemo {
	return &mediaAuthorityCompileMemo{entries: map[string]any{}}
}

type mediaAuthorityCompileMemoContextKey struct{}

func withMediaAuthorityCompileMemo(ctx context.Context, memo *mediaAuthorityCompileMemo) context.Context {
	return context.WithValue(ctx, mediaAuthorityCompileMemoContextKey{}, memo)
}

// memoizedCompileInput returns the value load produced for key within this
// claim, loading it once. Outside an obligation worker there is no memo and load
// runs every time. A failed load is not kept.
func memoizedCompileInput[T any](ctx context.Context, key string, load func() (T, error)) (T, error) {
	memo, _ := ctx.Value(mediaAuthorityCompileMemoContextKey{}).(*mediaAuthorityCompileMemo) //nolint:errcheck // absent means a compile outside the obligation worker
	if memo == nil {
		return load()
	}
	memo.mu.Lock()
	cached, ok := memo.entries[key]
	memo.mu.Unlock()
	if ok {
		if value, typed := cached.(T); typed {
			return value, nil
		}
	}
	loaded, err, _ := memo.group.Do(key, func() (any, error) {
		value, loadErr := load()
		if loadErr != nil {
			return nil, loadErr
		}
		memo.mu.Lock()
		memo.entries[key] = value
		memo.mu.Unlock()
		return value, nil
	})
	if err != nil {
		var zero T
		return zero, err
	}
	value, _ := loaded.(T) //nolint:errcheck // the group only ever stores T under this key
	return value, nil
}
