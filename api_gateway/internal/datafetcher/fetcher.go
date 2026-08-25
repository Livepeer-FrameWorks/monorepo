package datafetcher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/internal/loaders"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
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
	Logger         logging.Logger
	Caches         map[Service]*cache.Cache
	LoadTimeout    time.Duration
	OnLoadStarted  func(Service, string)
	OnLoadFinished func(Service, string, bool)
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
	logger      logging.Logger
	caches      map[Service]*cache.Cache
	loadTimeout time.Duration
	onLoadStart func(Service, string)
	onLoadDone  func(Service, string, bool)
}

const defaultLoadTimeout = 30 * time.Second

// New creates a new DataFetcher with the provided configuration.
func New(cfg Config) *DataFetcher {
	caches := make(map[Service]*cache.Cache)
	for svc, c := range cfg.Caches {
		if c != nil {
			caches[svc] = c
		}
	}
	loadTimeout := cfg.LoadTimeout
	if loadTimeout <= 0 {
		loadTimeout = defaultLoadTimeout
	}
	return &DataFetcher{
		logger:      cfg.Logger,
		caches:      caches,
		loadTimeout: loadTimeout,
		onLoadStart: cfg.OnLoadStarted,
		onLoadDone:  cfg.OnLoadFinished,
	}
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
		val, ok, err := cache.Get(ctx, key, func(loadCtx context.Context, _ string) (interface{}, bool, error) {
			operationCtx, cancel := context.WithTimeout(loadCtx, df.loadTimeout)
			defer cancel()
			if df.onLoadStart != nil {
				df.onLoadStart(req.Service, req.Operation)
			}
			if df.onLoadDone != nil {
				defer func() {
					df.onLoadDone(req.Service, req.Operation, errors.Is(operationCtx.Err(), context.DeadlineExceeded))
				}()
			}
			resp, err := req.Loader(operationCtx)
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
