package loaders

import (
	"context"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
)

// streamStatusTimeout bounds the live-status reads behind stream.metrics. They
// are point lookups resolved inside a user's GraphQL request, so a stalled
// Periscope fails the field within this bound instead of the client's
// general call timeout.
const streamStatusTimeout = 5 * time.Second

// StreamMetricsLoader loads stream metrics with request-scoped caching.
// Uses batch fetch when multiple streams are requested.
type StreamMetricsLoader struct {
	client periscope.Interface
	mu     sync.Mutex
	cache  map[string]*periscopepb.StreamStatusResponse // key: "tenantID:internalName"
}

// NewStreamMetricsLoader creates a new stream metrics loader
func NewStreamMetricsLoader(client periscope.Interface) *StreamMetricsLoader {
	return &StreamMetricsLoader{
		client: client,
		cache:  make(map[string]*periscopepb.StreamStatusResponse),
	}
}

// Load fetches metrics for a single stream, using cache if available
func (l *StreamMetricsLoader) Load(ctx context.Context, tenantID, internalName string) (*periscopepb.StreamStatusResponse, error) {
	key := tenantID + ":" + internalName

	l.mu.Lock()
	if cached, ok := l.cache[key]; ok {
		l.mu.Unlock()
		return cached, nil
	}
	l.mu.Unlock()

	readCtx, cancel := context.WithTimeout(ctx, streamStatusTimeout)
	defer cancel()
	resp, err := l.client.GetStreamStatus(readCtx, tenantID, internalName)
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	l.cache[key] = resp
	l.mu.Unlock()

	return resp, nil
}

// LoadMany fetches metrics for multiple streams in a single batch call
func (l *StreamMetricsLoader) LoadMany(ctx context.Context, tenantID string, internalNames []string) (map[string]*periscopepb.StreamStatusResponse, error) {
	results := make(map[string]*periscopepb.StreamStatusResponse)
	var toFetch []string

	l.mu.Lock()
	for _, name := range internalNames {
		key := tenantID + ":" + name
		if cached, ok := l.cache[key]; ok {
			results[name] = cached
		} else {
			toFetch = append(toFetch, name)
		}
	}
	l.mu.Unlock()

	if len(toFetch) == 0 {
		return results, nil
	}

	readCtx, cancel := context.WithTimeout(ctx, streamStatusTimeout)
	defer cancel()
	resp, err := l.client.GetStreamsStatus(readCtx, tenantID, toFetch)
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	for name, status := range resp.Statuses {
		key := tenantID + ":" + name
		l.cache[key] = status
		results[name] = status
	}
	l.mu.Unlock()

	return results, nil
}
