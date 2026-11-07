package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBlockCacheSlowRetiredDeletionDoesNotBlockSameAsset(t *testing.T) {
	s := New(Options{BasePath: t.TempDir(), BlockSize: 8})
	waitFillsOnCleanup(t, s)
	store := NewBlockStore(filepath.Join(t.TempDir(), "asset.mkv"), 8)
	if _, err := store.EnsureMeta("h", ".mkv", "old", 8); err != nil {
		t.Fatal(err)
	}
	oldFile, err := os.Create(store.tmpPath(0))
	if err != nil {
		t.Fatal(err)
	}
	defer oldFile.Close()
	info, err := oldFile.Stat()
	if err != nil {
		t.Fatal(err)
	}
	oldFill := &blockFill{tmpPath: store.tmpPath(0), blockPath: store.BlockPath(0), object: "old", diskInfo: info}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-7/8")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "NEW-BODY")
	}))
	defer upstream.Close()
	entered, release, invalidated := make(chan string, 1), make(chan struct{}), make(chan error, 1)
	store.removeAll = func(path string) error {
		entered <- path
		<-release
		return os.RemoveAll(path)
	}
	go func() { invalidated <- s.prepareBlockStore(store, "h", ".mkv", "new", 8) }()
	deleting := <-entered
	finished := make(chan error, 1)
	started := time.Now()
	go func() {
		other := NewBlockStore(store.assetPath, 8)
		if initErr := s.prepareBlockStore(other, "h", ".mkv", "new", 8); initErr != nil {
			finished <- initErr
			return
		}
		span := blockSpan{To: 7}
		fill, claimErr := s.claimBlockFill(context.Background(), other, span, 8, fillSource{object: "new", target: upstreamTarget{url: upstream.URL, client: upstream.Client()}})
		if claimErr != nil {
			finished <- claimErr
			return
		}
		var cold bytes.Buffer
		if _, readErr := readBlockFill(context.Background(), &cold, fill, span); readErr != nil {
			finished <- readErr
			return
		}
		if !eventuallyHasBlock(other, 0) {
			finished <- fmt.Errorf("replacement fill did not publish")
			return
		}
		var warm bytes.Buffer
		served, readErr := s.serveWarmBlock(&warm, other, span, "new", 8)
		if readErr != nil {
			finished <- readErr
			return
		}
		if cold.String() != "NEW-BODY" || !served || warm.String() != "NEW-BODY" {
			finished <- fmt.Errorf("replacement cold=%q warm=%q served=%v", cold.String(), warm.String(), served)
			return
		}
		if publishErr := s.publishBlockFill(other, oldFill, 8); publishErr == nil {
			finished <- fmt.Errorf("retired fill published into replacement generation")
			return
		}
		finished <- nil
	}()
	var result error
	var blocked bool
	select {
	case result = <-finished:
		t.Logf("same-asset prepare, claim, cold read, warm read and stale-publication fence completed in %s while retired deletion remained blocked", time.Since(started))
	case <-time.After(time.Second):
		blocked = true
	}
	close(release)
	if initErr := <-invalidated; initErr != nil {
		t.Error(initErr)
	}
	if blocked {
		<-finished
		t.Fatal("same asset's replacement generation blocked for 1s behind retired cache deletion")
	}
	if result != nil {
		t.Fatal(result)
	}
	s.fills.wg.Wait()
	if deleting == store.Dir() {
		t.Fatal("cleanup targeted the active cache directory")
	}
	if meta, metaErr := store.LoadMeta(); metaErr != nil || meta.Object != "new" || !store.HasBlock(0) {
		t.Fatalf("old generation cleanup removed replacement: meta=%+v err=%v", meta, metaErr)
	}
	if _, statErr := os.Stat(deleting); !os.IsNotExist(statErr) {
		t.Fatalf("retired generation remains after successful cleanup: %v", statErr)
	}
}

func TestBlockCacheRetiredDeletionFailureCannotDamageReplacement(t *testing.T) {
	store := NewBlockStore(filepath.Join(t.TempDir(), "asset.mkv"), 8)
	if _, err := store.EnsureMeta("h", ".mkv", "old", 8); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteBlock(0, []byte("OLD-BODY")); err != nil {
		t.Fatal(err)
	}
	deleteErr := errors.New("retired deletion denied")
	var retired string
	store.removeAll = func(path string) error {
		retired = path
		return deleteErr
	}
	meta, err := store.EnsureMeta("h", ".mkv", "new", 8)
	if !errors.Is(err, deleteErr) || meta == nil || meta.Object != "new" {
		t.Fatalf("replacement after cleanup failure: meta=%+v err=%v", meta, err)
	}
	if store.HasBlock(0) {
		t.Fatal("old block was blessed as replacement bytes after cleanup failure")
	}
	matched, matchErr := filepath.Match(filepath.Base(store.assetPath)+".retired-*.blocks", filepath.Base(retired))
	if matchErr != nil || !matched || filepath.Dir(retired) != filepath.Dir(store.Dir()) {
		t.Fatalf("retired path is not a unique sibling eligible for ordinary .blocks eviction: %q", retired)
	}
	if old, readErr := os.ReadFile(filepath.Join(retired, "0000000000.blk")); readErr != nil || string(old) != "OLD-BODY" {
		t.Fatalf("quarantined old bytes=%q err=%v", old, readErr)
	}
	if writeErr := store.WriteBlock(0, []byte("NEW-BODY")); writeErr != nil {
		t.Fatal(writeErr)
	}
	store.removeAll = os.RemoveAll
	if cleanupErr := store.removeRetired(retired); cleanupErr != nil {
		t.Fatal(cleanupErr)
	}
	if newBytes, readErr := os.ReadFile(store.BlockPath(0)); readErr != nil || string(newBytes) != "NEW-BODY" {
		t.Fatalf("retrying retired deletion changed active bytes=%q err=%v", newBytes, readErr)
	}
	if current, loadErr := store.LoadMeta(); loadErr != nil || current.Object != "new" {
		t.Fatalf("cleanup rolled back replacement identity: meta=%+v err=%v", current, loadErr)
	}
}

func TestBlockCacheMetadataFailureAfterDetachCannotRestoreOldBlocks(t *testing.T) {
	store := NewBlockStore(filepath.Join(t.TempDir(), "asset.mkv"), 8)
	if _, err := store.EnsureMeta("h", ".mkv", "old", 8); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteBlock(0, []byte("OLD-BODY")); err != nil {
		t.Fatal(err)
	}
	var retired string
	store.renameDir = func(from, to string) error {
		if err := os.Rename(from, to); err != nil {
			return err
		}
		retired = to
		return os.WriteFile(from, []byte("metadata write fault"), 0o644)
	}
	if _, err := store.EnsureMeta("h", ".mkv", "new", 8); err == nil {
		t.Fatal("replacement ignored a metadata creation failure")
	}
	if _, statErr := os.Stat(retired); !os.IsNotExist(statErr) {
		t.Fatalf("detached old generation was not cleaned after metadata failure: %v", statErr)
	}
	if marker, readErr := os.ReadFile(store.Dir()); readErr != nil || string(marker) != "metadata write fault" {
		t.Fatalf("old-generation cleanup touched active pathname: bytes=%q err=%v", marker, readErr)
	}
	if err := os.Remove(store.Dir()); err != nil {
		t.Fatal(err)
	}
	store.renameDir = os.Rename
	if meta, err := store.EnsureMeta("h", ".mkv", "new", 8); err != nil || meta.Object != "new" || store.HasBlock(0) {
		t.Fatalf("retry reused old blocks after metadata failure: meta=%+v err=%v", meta, err)
	}
}
