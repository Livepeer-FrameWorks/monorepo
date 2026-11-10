package relay

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	logrustest "github.com/sirupsen/logrus/hooks/test"
)

func TestBlockCacheRetirementReturnsBeforeDeletionAndTracksDrain(t *testing.T) {
	s := New(Options{BasePath: t.TempDir(), BlockSize: 8})
	store := NewBlockStore(filepath.Join(t.TempDir(), "asset.mkv"), 8)
	if _, err := store.prepareMetaFixture("h", ".mkv", "old", 8); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	store.removeAll = func(path string) error { close(entered); <-release; return os.RemoveAll(path) }
	returned := make(chan error, 1)
	go func() { returned <- s.prepareBlockStore(store, "h", ".mkv", "new", 8) }()
	<-entered
	var blocked bool
	select {
	case err := <-returned:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(time.Second):
		blocked = true
	}
	// A drain must include the deletion which is currently inside removeAll.
	drained := make(chan struct{})
	go func() { s.fills.wg.Wait(); close(drained) }()
	var untracked bool
	select {
	case <-drained:
		untracked = true
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if blocked {
		<-returned
	}
	<-drained
	if blocked {
		t.Error("triggering viewer waited for retired directory deletion")
	}
	if untracked {
		t.Error("retired directory deletion was absent from the fill drain")
	}
}

func TestBlockCacheRetirementBoundsDeletionWork(t *testing.T) {
	s := New(Options{BasePath: t.TempDir(), BlockSize: 8})
	release := make(chan struct{})
	t.Cleanup(s.fills.wg.Wait)
	t.Cleanup(func() { close(release) })
	for i := 0; i < 2; i++ {
		store := NewBlockStore(filepath.Join(t.TempDir(), "asset.mkv"), 8)
		if _, err := store.prepareMetaFixture("h", ".mkv", "old", 8); err != nil {
			t.Fatal(err)
		}
		entered := make(chan struct{})
		store.removeAll = func(path string) error { close(entered); <-release; return os.RemoveAll(path) }
		if err := s.prepareBlockStore(store, "h", ".mkv", "new", 8); err != nil {
			t.Fatal(err)
		}
		<-entered
	}
	store := NewBlockStore(filepath.Join(t.TempDir(), "asset.mkv"), 8)
	if _, err := store.prepareMetaFixture("h", ".mkv", "old", 8); err != nil {
		t.Fatal(err)
	}
	unexpected := make(chan struct{}, 1)
	store.removeAll = func(string) error { unexpected <- struct{}{}; return nil }
	if err := s.prepareBlockStore(store, "h", ".mkv", "new", 8); err != nil {
		t.Fatal(err)
	}
	retired, err := filepath.Glob(store.assetPath + ".retired-*.blocks")
	if err != nil || len(retired) != 1 {
		t.Fatalf("saturated cleanup lost garbage ownership: paths=%v err=%v", retired, err)
	}
	if len(s.fills.retiredCleanup) != 2 {
		t.Fatalf("cleanup admission=%d, want 2", len(s.fills.retiredCleanup))
	}
	select {
	case <-unexpected:
		t.Fatal("saturated cleanup launched additional deletion")
	default:
	}
}

func TestBlockCacheRetirementCleansAfterMetadataFailure(t *testing.T) {
	s := New(Options{BasePath: t.TempDir(), BlockSize: 8})
	store := NewBlockStore(filepath.Join(t.TempDir(), "asset.mkv"), 8)
	if _, err := store.prepareMetaFixture("h", ".mkv", "old", 8); err != nil {
		t.Fatal(err)
	}
	var retired string
	store.renameDir = func(from, to string) error {
		if err := os.Rename(from, to); err != nil {
			return err
		}
		retired = to
		return os.WriteFile(from, []byte("metadata fault"), 0o644)
	}
	if err := s.prepareBlockStore(store, "h", ".mkv", "new", 8); err == nil {
		t.Fatal("metadata failure was ignored")
	}
	s.fills.wg.Wait()
	if _, err := os.Stat(retired); !os.IsNotExist(err) {
		t.Fatalf("metadata failure left detached cleanup unscheduled: %v", err)
	}
	if b, err := os.ReadFile(store.Dir()); err != nil || string(b) != "metadata fault" {
		t.Fatalf("cleanup changed active path: %q %v", b, err)
	}
}

func TestRetiredBlockCacheNames(t *testing.T) {
	for _, name := range []string{"asset.mkv.retired-123.blocks", "asset.mp4.retired-456.blocks"} {
		if !IsRetiredBlockCache(filepath.Join(t.TempDir(), name)) {
			t.Errorf("retired cache not recognized: %s", name)
		}
	}
	for _, name := range []string{"asset.mkv.blocks", "asset.retired-.blocks", "asset.retired-123.blocks", "asset.mkv.retired-123.blocks.tmp"} {
		if IsRetiredBlockCache(name) {
			t.Errorf("active/unrelated path classified as retired: %s", name)
		}
	}
}

func TestBlockCacheRetirementDeleteFailureOnlyWarns(t *testing.T) {
	logger, hook := logrustest.NewNullLogger()
	s := New(Options{BasePath: t.TempDir(), BlockSize: 8, Logger: logger})
	store := NewBlockStore(filepath.Join(t.TempDir(), "asset.mkv"), 8)
	if _, err := store.prepareMetaFixture("h", ".mkv", "old", 8); err != nil {
		t.Fatal(err)
	}
	deleteErr := errors.New("retired deletion denied")
	store.removeAll = func(string) error { return deleteErr }
	if err := s.prepareBlockStore(store, "h", ".mkv", "new", 8); err != nil {
		t.Errorf("cleanup failure escaped into viewer: %v", err)
	}
	s.fills.wg.Wait()
	if meta, err := store.LoadMeta(); err != nil || meta.Object != "new" {
		t.Fatalf("replacement identity=%+v err=%v", meta, err)
	}
	found := false
	for _, entry := range hook.AllEntries() {
		if err, ok := entry.Data["error"].(error); ok && errors.Is(err, deleteErr) {
			found = true
		}
	}
	if !found {
		t.Error("retired deletion failure was not logged")
	}
}

func TestBlockCacheRetirementToleratesConcurrentEviction(t *testing.T) {
	store := NewBlockStore(filepath.Join(t.TempDir(), "asset.mkv"), 8)
	if _, err := store.prepareMetaFixture("h", ".mkv", "old", 8); err != nil {
		t.Fatal(err)
	}
	store.renameDir = func(from, to string) error {
		if err := os.RemoveAll(from); err != nil {
			return err
		}
		return &os.LinkError{Op: "rename", Old: from, New: to, Err: os.ErrNotExist}
	}
	meta, err := store.prepareMetaFixture("h", ".mkv", "new", 8)
	if err != nil || meta == nil || meta.Object != "new" {
		t.Fatalf("concurrent eviction prevented fresh metadata: meta=%+v err=%v", meta, err)
	}
}
