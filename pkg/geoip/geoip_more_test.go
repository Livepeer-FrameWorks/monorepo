package geoip

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestReaderFileChangedNilOld(t *testing.T) {
	if got := readerFileChanged("", nil); got != false {
		t.Fatalf("readerFileChanged(\"\", nil) = %v, want false", got)
	}
	if got := readerFileChanged("/nonexistent/path.mmdb", nil); got != true {
		t.Fatalf("readerFileChanged(nonempty, nil) = %v, want true", got)
	}
}

func TestReaderFileChangedWithOldInfo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db.mmdb")
	if err := os.WriteFile(path, []byte("aaa"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	info := statReaderPath(path)
	if info == nil {
		t.Fatal("expected file info")
	}
	if got := readerFileChanged(path, info); got != false {
		t.Fatalf("unchanged file readerFileChanged = %v, want false", got)
	}
	if got := readerFileChanged("/nonexistent/path.mmdb", info); got != false {
		t.Fatalf("missing file with old info = %v, want false", got)
	}
}

func TestReaderInfoChangedNilOldReturnsHasNext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db.mmdb")
	if err := os.WriteFile(path, []byte("aaa"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	info := statReaderPath(path)
	if info == nil {
		t.Fatal("expected file info")
	}
	if got := readerInfoChanged(info, nil); got != true {
		t.Fatalf("readerInfoChanged(info, nil) = %v, want true", got)
	}
}

func TestReaderInfoChangedSizeOnlyDifference(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "db.mmdb")
	if err := os.WriteFile(path, []byte("aaa"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	oldInfo := statReaderPath(path)
	if oldInfo == nil {
		t.Fatal("expected old info")
	}

	if err := os.WriteFile(path, []byte("aaaaaaaa"), 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	// Force identical ModTime so the size term is the only thing that differs;
	// SameFile stays true because it is the same path/inode.
	if err := os.Chtimes(path, oldInfo.ModTime(), oldInfo.ModTime()); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	newInfo := statReaderPath(path)
	if newInfo == nil {
		t.Fatal("expected new info")
	}
	if newInfo.Size() == oldInfo.Size() {
		t.Fatalf("precondition: sizes should differ, both %d", newInfo.Size())
	}
	if !newInfo.ModTime().Equal(oldInfo.ModTime()) {
		t.Fatalf("precondition: ModTimes should be equal")
	}
	if !os.SameFile(newInfo, oldInfo) {
		t.Fatalf("precondition: SameFile should be true")
	}
	if got := readerInfoChanged(newInfo, oldInfo); got != true {
		t.Fatalf("size-only change readerInfoChanged = %v, want true", got)
	}
}

func TestLookupCityReloadErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "GeoLite2-City.mmdb")
	// Junk content: exists on disk but is not a valid MMDB, so reload's
	// geoip2.Open fails and reloadIfChanged returns a real error.
	if err := os.WriteFile(path, []byte("not-a-real-mmdb"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	r := &Reader{dbPath: path}
	record, err := r.lookupCity(net.ParseIP("8.8.8.8"))
	if err == nil {
		t.Fatal("expected reload error from lookupCity, got nil")
	}
	if record != nil {
		t.Fatalf("expected nil record on reload error, got %+v", record)
	}
}
