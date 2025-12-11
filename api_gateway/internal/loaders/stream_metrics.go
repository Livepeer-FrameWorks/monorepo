package loaders

import (
	"context"
	"sync"

	"frameworks/pkg/clients/periscope"
	pb "frameworks/pkg/proto"
)

// StreamMetricsLoader loads stream metrics with request-scoped caching.
// Uses batch fetch when multiple streams are requested.
type StreamMetricsLoader struct {
	client *periscope.GRPCClient
	mu     sync.Mutex
	cache  map[string]*pb.StreamStatusResponse // key: "tenantID:internalName"
}

// NewStreamMetricsLoader creates a new stream metrics loader
func NewStreamMetricsLoader(client *periscope.GRPCClient) *StreamMetricsLoader {
	return &StreamMetricsLoader{
		client: client,
		cache:  make(map[string]*pb.StreamStatusResponse),
	}
}

// Load fetches metrics for a single stream, using cache if available
func (l *StreamMetricsLoader) Load(ctx context.Context, tenantID, internalName string) (*pb.StreamStatusResponse, error) {
	key := tenantID + ":" + internalName

	l.mu.Lock()
	if cached, ok := l.cache[key]; ok {
		l.mu.Unlock()
		return cached, nil
	}
	l.mu.Unlock()

	// Fetch from Periscope
	resp, err := l.client.GetStreamStatus(ctx, tenantID, internalName)
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	l.cache[key] = resp
	l.mu.Unlock()

	return resp, nil
}

// LoadMany fetches metrics for multiple streams in a single batch call
func (l *StreamMetricsLoader) LoadMany(ctx context.Context, tenantID string, internalNames []string) (map[string]*pb.StreamStatusResponse, error) {
	results := make(map[string]*pb.StreamStatusResponse)
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

	// Batch fetch from Periscope
	resp, err := l.client.GetStreamsStatus(ctx, tenantID, toFetch)
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
