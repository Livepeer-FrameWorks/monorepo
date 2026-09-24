package loaders

import (
	"context"
	"sync"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/bosun"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"
)

// WebhookAttemptsLoader loads the attempt history of webhook deliveries. A
// deliveries listing registers its page; the first attemptHistory field
// resolved on that page then fetches every registered delivery in one Bosun
// call, and its siblings read the cache. Bosun scopes the call to the tenant
// on the context, so one loader serves one request of one tenant.
type WebhookAttemptsLoader struct {
	client bosun.Interface

	// fetchMu serializes fetches so concurrent sibling resolvers wait for the
	// batch in flight instead of issuing their own.
	fetchMu sync.Mutex
	mu      sync.Mutex
	pending []string
	cache   map[string][]*bosunpb.WebhookDeliveryAttempt
}

// NewWebhookAttemptsLoader creates a loader backed by the Bosun client.
func NewWebhookAttemptsLoader(client bosun.Interface) *WebhookAttemptsLoader {
	return &WebhookAttemptsLoader{client: client, cache: make(map[string][]*bosunpb.WebhookDeliveryAttempt)}
}

// Register queues delivery IDs for the next batched fetch.
func (l *WebhookAttemptsLoader) Register(deliveryIDs ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, id := range deliveryIDs {
		if _, ok := l.cache[id]; !ok && id != "" {
			l.pending = append(l.pending, id)
		}
	}
}

// Load returns the attempts of one delivery, oldest first. A delivery with no
// attempts, or one the tenant does not own, has none.
func (l *WebhookAttemptsLoader) Load(ctx context.Context, deliveryID string) ([]*bosunpb.WebhookDeliveryAttempt, error) {
	if attempts, ok := l.cached(deliveryID); ok {
		return attempts, nil
	}
	l.fetchMu.Lock()
	defer l.fetchMu.Unlock()
	if attempts, ok := l.cached(deliveryID); ok {
		return attempts, nil
	}

	l.mu.Lock()
	batch := make([]string, 0, len(l.pending)+1)
	seen := map[string]bool{}
	for _, id := range append(l.pending, deliveryID) {
		if _, done := l.cache[id]; done || seen[id] {
			continue
		}
		seen[id] = true
		batch = append(batch, id)
	}
	l.pending = nil
	l.mu.Unlock()

	for start := 0; start < len(batch); start += pagination.MaxLimit {
		chunk := batch[start:min(start+pagination.MaxLimit, len(batch))]
		resp, err := l.client.ListAttemptsForDeliveries(ctx, chunk)
		if err != nil {
			// The unfetched IDs stay pending so a later field can retry them.
			l.Register(batch[start:]...)
			return nil, err
		}
		fetched := make(map[string][]*bosunpb.WebhookDeliveryAttempt, len(chunk))
		for _, d := range resp.GetDeliveries() {
			fetched[d.GetDeliveryId()] = d.GetAttempts()
		}
		l.mu.Lock()
		for _, id := range chunk {
			l.cache[id] = fetched[id]
		}
		l.mu.Unlock()
	}
	attempts, _ := l.cached(deliveryID)
	return attempts, nil
}

func (l *WebhookAttemptsLoader) cached(deliveryID string) ([]*bosunpb.WebhookDeliveryAttempt, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	attempts, ok := l.cache[deliveryID]
	return attempts, ok
}
