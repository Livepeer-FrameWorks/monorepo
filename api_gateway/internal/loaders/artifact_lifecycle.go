package loaders

import (
	"context"
	"sync"

	"frameworks/pkg/clients/periscope"
	pb "frameworks/pkg/proto"
)

// ArtifactLifecycleLoader loads artifact lifecycle data with request-scoped caching.
// Used by GraphQL field resolvers to efficiently batch-fetch lifecycle data from Periscope.
type ArtifactLifecycleLoader struct {
	client *periscope.GRPCClient
	mu     sync.Mutex
	cache  map[string]*pb.ArtifactState // key: "tenantID:requestID"
}

// NewArtifactLifecycleLoader creates a new artifact lifecycle loader
func NewArtifactLifecycleLoader(client *periscope.GRPCClient) *ArtifactLifecycleLoader {
	return &ArtifactLifecycleLoader{
		client: client,
		cache:  make(map[string]*pb.ArtifactState),
	}
}

// Load fetches lifecycle data for a single artifact, using cache if available
func (l *ArtifactLifecycleLoader) Load(ctx context.Context, tenantID, requestID string) (*pb.ArtifactState, error) {
	key := tenantID + ":" + requestID

	l.mu.Lock()
	if cached, ok := l.cache[key]; ok {
		l.mu.Unlock()
		return cached, nil
	}
	l.mu.Unlock()

	// Fetch single item from Periscope
	resp, err := l.client.GetArtifactStatesByIDs(ctx, tenantID, []string{requestID}, nil)
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Cache and return the result
	if len(resp.Artifacts) > 0 {
		state := resp.Artifacts[0]
		l.cache[key] = state
		return state, nil
	}

	// Cache nil result to avoid repeated lookups for missing items
	l.cache[key] = nil
	return nil, nil
}

// LoadMany fetches lifecycle data for multiple artifacts in a single batch call
func (l *ArtifactLifecycleLoader) LoadMany(ctx context.Context, tenantID string, requestIDs []string) (map[string]*pb.ArtifactState, error) {
	results := make(map[string]*pb.ArtifactState)
	var toFetch []string

	l.mu.Lock()
	for _, id := range requestIDs {
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

	// Batch fetch from Periscope
	resp, err := l.client.GetArtifactStatesByIDs(ctx, tenantID, toFetch, nil)
	if err != nil {
		return nil, err
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Index response by request_id
	statesByID := make(map[string]*pb.ArtifactState)
	for _, state := range resp.Artifacts {
		statesByID[state.RequestId] = state
	}

	// Cache results and populate return map
	for _, id := range toFetch {
		key := tenantID + ":" + id
		state := statesByID[id] // may be nil if not found
		l.cache[key] = state
		results[id] = state
	}

	return results, nil
}

// Prime adds an artifact state to the cache (used when parent resolver pre-fetches)
func (l *ArtifactLifecycleLoader) Prime(tenantID string, state *pb.ArtifactState) {
	if state == nil || state.RequestId == "" {
		return
	}
	key := tenantID + ":" + state.RequestId
	l.mu.Lock()
	l.cache[key] = state
	l.mu.Unlock()
}

// PrimeMany adds multiple artifact states to the cache
func (l *ArtifactLifecycleLoader) PrimeMany(tenantID string, states []*pb.ArtifactState) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, state := range states {
		if state == nil || state.RequestId == "" {
			continue
		}
		key := tenantID + ":" + state.RequestId
		l.cache[key] = state
	}
}

// Context key for storing the loader
type artifactLoaderKey struct{}

// WithArtifactLoader stores an artifact loader in the context
func WithArtifactLoader(ctx context.Context, loader *ArtifactLifecycleLoader) context.Context {
	return context.WithValue(ctx, artifactLoaderKey{}, loader)
}

// ArtifactLoaderFromContext retrieves the artifact loader from context
func ArtifactLoaderFromContext(ctx context.Context) *ArtifactLifecycleLoader {
	loader, _ := ctx.Value(artifactLoaderKey{}).(*ArtifactLifecycleLoader)
	return loader
}
