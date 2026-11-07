package relay

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

func TestBlockCacheSlowInvalidationDoesNotBlockOtherStores(t *testing.T) {
	s := New(Options{BasePath: t.TempDir(), BlockSize: 8})
	waitFillsOnCleanup(t, s)
	newStore := func(name string) *BlockStore {
		store := NewBlockStore(filepath.Join(t.TempDir(), name), 8)
		if _, err := store.EnsureMeta("h", ".mkv", "old", 8); err != nil {
			t.Fatal(err)
		}
		return store
	}
	slow := newStore("slow.mkv")
	warm := newStore("warm.mkv")
	if err := warm.WriteBlock(0, []byte("OLD-BODY")); err != nil {
		t.Fatal(err)
	}
	cold := newStore("cold.mkv")
	publication := newStore("publication.mkv")
	if err := os.WriteFile(publication.tmpPath(0), []byte("OLD-BODY"), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(publication.tmpPath(0))
	if err != nil {
		t.Fatal(err)
	}
	fill := &blockFill{tmpPath: publication.tmpPath(0), blockPath: publication.BlockPath(0), object: "old", diskInfo: info}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-7/8")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "OLD-BODY")
	}))
	defer upstream.Close()
	src := fillSource{object: "old", target: upstreamTarget{url: upstream.URL, client: upstream.Client()}}
	entered, release, invalidated := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	slow.removeAll = func(path string) error {
		close(entered)
		<-release
		return os.RemoveAll(path)
	}
	go func() { invalidated <- s.prepareBlockStore(slow, "h", ".mkv", "new", 8) }()
	<-entered
	operations := map[string]func() error{
		"warm read": func() error {
			var got bytes.Buffer
			served, readErr := s.serveWarmBlock(&got, warm, blockSpan{To: 7}, "old", 8)
			if readErr != nil {
				return fmt.Errorf("warm read: %w", readErr)
			}
			if !served || got.String() != "OLD-BODY" {
				return fmt.Errorf("warm read served=%v bytes=%q", served, got.String())
			}
			return nil
		},
		"disk claim": func() error {
			_, claimErr := s.claimBlockFill(context.Background(), cold, blockSpan{To: 7}, 8, src)
			return claimErr
		},
		"publication": func() error { return s.publishBlockFill(publication, fill, 8) },
		"fill bookkeeping": func() error {
			s.fills.running("another cache")
			return nil
		},
	}
	type outcome struct {
		name string
		err  error
		wait time.Duration
	}
	finished := make(chan outcome, len(operations))
	for name, run := range operations {
		go func() {
			start := time.Now()
			runErr := run()
			finished <- outcome{name: name, err: runErr, wait: time.Since(start)}
		}()
	}
	pending := make(map[string]bool, len(operations))
	for name := range operations {
		pending[name] = true
	}
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	waiting := true
	for len(pending) > 0 && waiting {
		select {
		case result := <-finished:
			delete(pending, result.name)
			if result.err != nil {
				t.Errorf("%s failed: %v", result.name, result.err)
			}
			t.Logf("%s completed during blocked invalidation in %s", result.name, result.wait)
		case <-deadline.C:
			waiting = false
		}
	}
	close(release)
	if invalidateErr := <-invalidated; invalidateErr != nil {
		t.Error(invalidateErr)
	}
	if len(pending) > 0 {
		blocked := make([]string, 0, len(pending))
		for name := range pending {
			blocked = append(blocked, name)
		}
		sort.Strings(blocked)
		for range pending {
			<-finished
		}
		t.Fatalf("unrelated stores blocked for 1s behind slow invalidation: %v", blocked)
	}
	s.fills.wg.Wait()
	s.fills.mu.Lock()
	remaining := len(s.fills.stores)
	s.fills.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("completed operations retained %d directory locks", remaining)
	}
}

func TestBlockCacheReplacementSerializesSeparateHandlesOfSameDirectory(t *testing.T) {
	s := New(Options{BasePath: t.TempDir(), BlockSize: 8})
	path := filepath.Join(t.TempDir(), "asset.mkv")
	store := NewBlockStore(path, 8)
	if _, err := store.EnsureMeta("h", ".mkv", "old", 8); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteBlock(0, []byte("OLD-BODY")); err != nil {
		t.Fatal(err)
	}
	entered, release, invalidated := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	store.removeAll = func(dir string) error {
		close(entered)
		<-release
		return os.RemoveAll(dir)
	}
	go func() { invalidated <- s.prepareBlockStore(store, "h", ".mkv", "new", 8) }()
	<-entered
	otherHandle := NewBlockStore(path, 8)
	finished := make(chan error, 1)
	go func() {
		var got bytes.Buffer
		served, err := s.serveWarmBlock(&got, otherHandle, blockSpan{To: 7}, "old", 8)
		if err != nil {
			finished <- fmt.Errorf("replaced cache read: %w", err)
			return
		}
		if served || got.Len() != 0 {
			finished <- fmt.Errorf("replaced cache served=%v bytes=%q", served, got.String())
			return
		}
		finished <- nil
	}()
	var early bool
	select {
	case <-finished:
		early = true
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-invalidated; err != nil {
		t.Error(err)
	}
	if early {
		t.Fatal("separate handle bypassed the directory's active identity replacement")
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	s.fills.mu.Lock()
	remaining := len(s.fills.stores)
	s.fills.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("completed replacement retained %d directory locks", remaining)
	}
}
