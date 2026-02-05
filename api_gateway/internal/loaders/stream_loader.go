package loaders

import (
	"context"
	"sync"

	"frameworks/pkg/clients/commodore"
	pb "frameworks/pkg/proto"
)

// StreamLoader loads streams with request-scoped caching.
// Used by GraphQL field resolvers to efficiently batch-fetch streams from Commodore.
type StreamLoader struct {
	client *commodore.GRPCClient
	mu     sync.Mutex
	cache  map[string]*pb.Stream // key: "tenantID:streamID"
}

// NewStreamLoader creates a new stream loader
func NewStreamLoader(client *commodore.GRPCClient) *StreamLoader {
	return &StreamLoader{
		client: client,
		cache:  make(map[string]*pb.Stream),
	}
}

// Load fetches a single stream, using cache if available
func (l *StreamLoader) Load(ctx context.Context, tenantID, streamID string) (*pb.Stream, error) {
	key := tenantID + ":" + streamID

	l.mu.Lock()
	if cached, ok := l.cache[key]; ok {
		l.mu.Unlock()
		return cached, nil
	}
	l.mu.Unlock()

	// Fetch single stream from Commodore
	stream, err := l.client.GetStream(ctx, streamID)
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	l.cache[key] = stream
	l.mu.Unlock()

	return stream, nil
}

// LoadMany fetches multiple streams in a single batch call
func (l *StreamLoader) LoadMany(ctx context.Context, tenantID string, streamIDs []string) (map[string]*pb.Stream, error) {
	results := make(map[string]*pb.Stream)
	var toFetch []string

	l.mu.Lock()
	for _, id := range streamIDs {
		key := tenantID + ":" + id
		if cached, ok := l.cache[key]; ok {
			results[id] = cached
		} else {
			toFetch = append(toFetch, id)
		}
	}
	l.mu.Unlock()

	if len(toFetch) == 0 {
		return results, nil
	}

	// Batch fetch from Commodore
	resp, err := l.client.GetStreamsBatch(ctx, toFetch)
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Index response by stream_id
	streamsByID := make(map[string]*pb.Stream)
	for _, stream := range resp.Streams {
		streamsByID[stream.StreamId] = stream
	}

	// Cache results and populate return map
	for _, id := range toFetch {
		key := tenantID + ":" + id
		stream := streamsByID[id] // may be nil if not found
		l.cache[key] = stream
		results[id] = stream
	}

	return results, nil
}

// Prime adds a stream to the cache (used when parent resolver pre-fetches)
func (l *StreamLoader) Prime(tenantID string, stream *pb.Stream) {
	if stream == nil || stream.StreamId == "" {
		return
	}
	key := tenantID + ":" + stream.StreamId
	l.mu.Lock()
	l.cache[key] = stream
	l.mu.Unlock()
}

// PrimeMany adds multiple streams to the cache
func (l *StreamLoader) PrimeMany(tenantID string, streams []*pb.Stream) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, stream := range streams {
		if stream == nil || stream.StreamId == "" {
			continue
		}
		key := tenantID + ":" + stream.StreamId
		l.cache[key] = stream
	}
}

// PreloadStreams batch-fetches streams into the request-scoped cache.
// Called by connection builders before GraphQL resolves child stream fields.
func PreloadStreams(ctx context.Context, tenantID string, streamIDs []string) {
	l := FromContext(ctx)
	if l == nil || l.Stream == nil || len(streamIDs) == 0 {
		return
	}
	seen := make(map[string]bool, len(streamIDs))
	unique := make([]string, 0, len(streamIDs))
	for _, id := range streamIDs {
		if id != "" && !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	if len(unique) > 0 {
		_, _ = l.Stream.LoadMany(ctx, tenantID, unique)
	}
}

// PrimeNil marks stream IDs as missing to avoid redundant retries.
func (l *StreamLoader) PrimeNil(tenantID string, streamIDs []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, id := range streamIDs {
		if id == "" {
			continue
		}
		key := tenantID + ":" + id
		l.cache[key] = nil
	}
}
