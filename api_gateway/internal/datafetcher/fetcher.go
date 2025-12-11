package datafetcher

import (
	"context"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/loaders"
	"frameworks/pkg/cache"
	"frameworks/pkg/logging"
)

// Service identifies a downstream dependency.
type Service string

const (
	// ServicePeriscope refers to the analytics service.
	ServicePeriscope Service = "periscope"
	// ServiceQuartermaster refers to the infrastructure service.
	ServiceQuartermaster Service = "quartermaster"
	// ServiceCommodore refers to the stream control plane.
	ServiceCommodore Service = "commodore"
)

// Config controls DataFetcher construction.
type Config struct {
	Logger logging.Logger
	Caches map[Service]*cache.Cache
}

// FetchRequest describes a downstream fetch.
type FetchRequest struct {
	Service   Service
	Operation string
	KeyParts  []string
	SkipMemo  bool
	SkipCache bool
	Loader    func(context.Context) (interface{}, error)
}

// DataFetcher coordinates memoization and shared caches.
type DataFetcher struct {
	logger logging.Logger
	caches map[Service]*cache.Cache
}

// New creates a new DataFetcher with the provided configuration.
func New(cfg Config) *DataFetcher {
	caches := make(map[Service]*cache.Cache)
	for svc, c := range cfg.Caches {
		if c != nil {
			caches[svc] = c
		}
	}
	return &DataFetcher{logger: cfg.Logger, caches: caches}
}

// Fetch executes the request while enforcing memoization and cache reuse.
func (df *DataFetcher) Fetch(ctx context.Context, req FetchRequest) (interface{}, error) {
	if req.Loader == nil {
		return nil, fmt.Errorf("datafetcher: loader required for %s/%s", req.Service, req.Operation)
	}

	key := df.buildKey(req)

	fetch := func() (interface{}, error) {
		return df.fetchWithCache(ctx, key, req)
	}

	if req.SkipMemo {
		return fetch()
	}

	if lds := loaders.FromContext(ctx); lds != nil && lds.Memo != nil {
		return lds.Memo.GetOrLoad("fetch:"+key, fetch)
	}

	return fetch()
}

func (df *DataFetcher) fetchWithCache(ctx context.Context, key string, req FetchRequest) (interface{}, error) {
	if req.SkipCache {
		return req.Loader(ctx)
	}
	if cache := df.caches[req.Service]; cache != nil {
		val, ok, err := cache.Get(ctx, key, func(context.Context, string) (interface{}, bool, error) {
			resp, err := req.Loader(ctx)
			if err != nil {
				return nil, false, err
			}
			return resp, true, nil
		})
		if err != nil {
			return nil, err
		}
		if ok {
			return val, nil
		}
	}
	return req.Loader(ctx)
}

func (df *DataFetcher) buildKey(req FetchRequest) string {
	parts := []string{string(req.Service), req.Operation}
	parts = append(parts, req.KeyParts...)
	return strings.Join(parts, "|")
}
