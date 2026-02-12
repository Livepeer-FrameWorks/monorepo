package grpc

import (
	"sync"
	"testing"

	"frameworks/pkg/logging"
)

func TestTryBeginOriginPullDeduplicatesConcurrentCalls(t *testing.T) {
	server := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)

	const workers = 32
	var wg sync.WaitGroup
	wg.Add(workers)
	results := make(chan bool, workers)

	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			results <- server.tryBeginOriginPull("tenant+stream")
		}()
	}
	wg.Wait()
	close(results)

	successes := 0
	for ok := range results {
		if ok {
			successes++
		}
	}
	if successes != 1 {
		t.Fatalf("expected exactly one winner, got %d", successes)
	}

	server.finishOriginPull("tenant+stream")
	if !server.tryBeginOriginPull("tenant+stream") {
		t.Fatal("expected lock to be released after finishOriginPull")
	}
}
