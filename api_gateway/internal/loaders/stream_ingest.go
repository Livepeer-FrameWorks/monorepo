package loaders

import (
	"context"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ingestEntry struct {
	view *pb.StreamIngest
	err  error
}

// StreamIngestLoader caches both state and per-stream cell failures for one request.
type StreamIngestLoader struct {
	client commodore.Interface
	mu     sync.Mutex
	cache  map[string]ingestEntry
}

func NewStreamIngestLoader(client commodore.Interface) *StreamIngestLoader {
	return &StreamIngestLoader{client: client, cache: map[string]ingestEntry{}}
}

func (l *StreamIngestLoader) LoadMany(ctx context.Context, tenant string, ids []string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	missing := []string{}
	seen := map[string]bool{}
	for _, id := range ids {
		if _, ok := l.cache[tenant+":"+id]; !ok && !seen[id] {
			missing = append(missing, id)
			seen[id] = true
		}
	}
	if len(missing) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	response, err := l.client.GetStreamIngestStates(ctx, missing)
	unavailable := map[string]bool{}
	for _, id := range response.GetUnavailableStreamIds() {
		unavailable[id] = true
	}
	for _, id := range missing {
		entry := ingestEntry{err: err}
		if err == nil {
			entry.view = response.GetStates()[id]
			if unavailable[id] {
				entry.err = status.Error(codes.Unavailable, "stream ingest cell unavailable")
			} else if entry.view == nil {
				entry.err = status.Error(codes.NotFound, "stream not found")
			}
		}
		l.cache[tenant+":"+id] = entry
	}
	return err
}

func (l *StreamIngestLoader) Load(ctx context.Context, tenant, id string) (*pb.StreamIngest, error) {
	if err := l.LoadMany(ctx, tenant, []string{id}); err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.cache[tenant+":"+id]
	return entry.view, entry.err
}
