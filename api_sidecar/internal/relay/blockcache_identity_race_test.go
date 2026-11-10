package relay

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"frameworks/api_sidecar/internal/admission"
)

func TestBlockCacheReplacementDoesNotBlessFailedInvalidation(t *testing.T) {
	store := NewBlockStore(filepath.Join(t.TempDir(), "asset.mkv"), 8)
	if _, err := store.prepareMetaFixture("h", ".mkv", "old", 8); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteBlock(0, []byte("OLD-BODY")); err != nil {
		t.Fatal(err)
	}
	renameErr := errors.New("rename denied")
	store.renameDir = func(string, string) error { return renameErr }
	if _, err := store.prepareMetaFixture("h", ".mkv", "new", 8); !errors.Is(err, renameErr) {
		t.Fatalf("invalidation error=%v, want %v", err, renameErr)
	}
	meta, err := store.LoadMeta()
	if err != nil || meta.Object != "old" {
		t.Fatalf("failed invalidation replaced source identity: meta=%+v err=%v", meta, err)
	}
	retired, globErr := filepath.Glob(store.assetPath + ".retired-*.blocks")
	if globErr != nil || len(retired) != 0 {
		t.Fatalf("failed rename left retired generations: %v err=%v", retired, globErr)
	}
}

func TestBlockCacheReplacementRetryDropsPartiallyInvalidatedBlocks(t *testing.T) {
	store := NewBlockStore(filepath.Join(t.TempDir(), "asset.mkv"), 8)
	if _, err := store.prepareMetaFixture("h", ".mkv", "old", 8); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteBlock(0, []byte("OLD-BODY")); err != nil {
		t.Fatal(err)
	}
	renameErr := errors.New("rename denied")
	store.renameDir = func(string, string) error { return renameErr }
	if _, err := store.prepareMetaFixture("h", ".mkv", "new", 8); !errors.Is(err, renameErr) {
		t.Fatalf("first invalidation error=%v, want %v", err, renameErr)
	}
	if err := os.Remove(store.MetaPath()); err != nil {
		t.Fatal(err)
	}
	store.renameDir = os.Rename
	meta, err := store.prepareMetaFixture("h", ".mkv", "new", 8)
	if err != nil || meta.Object != "new" {
		t.Fatalf("replacement retry: meta=%+v err=%v", meta, err)
	}
	if store.HasBlock(0) {
		t.Fatal("replacement retry blessed old block after partial invalidation removed its metadata")
	}
}

func TestObsoleteBlockFillCannotPublishOrRemoveReplacementTmp(t *testing.T) {
	s := New(Options{BasePath: t.TempDir(), BlockSize: 8})
	store := NewBlockStore(filepath.Join(t.TempDir(), "asset.mkv"), 8)
	if _, err := store.prepareMetaFixture("h", ".mkv", "old", 8); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(store.tmpPath(0), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	fileInfo, err := file.Stat()
	if err != nil {
		t.Fatal(err)
	}
	fill := &blockFill{tmpPath: store.tmpPath(0), blockPath: store.BlockPath(0), object: "old", diskInfo: fileInfo, changed: make(chan struct{})}
	if _, prepareErr := store.prepareMetaFixture("h", ".mkv", "new", 8); prepareErr != nil {
		t.Fatal(prepareErr)
	}
	if writeErr := os.WriteFile(store.tmpPath(0), []byte("NEW-BODY"), 0o644); writeErr != nil {
		t.Fatal(writeErr)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Range", "bytes 0-7/8")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "OLD-BODY")
	}))
	defer upstream.Close()
	err = s.runBlockFill(context.Background(), fill, file, store, 0, 0, 7, 8, 0, fillSource{object: "old", target: upstreamTarget{url: upstream.URL, client: upstream.Client()}})
	if err == nil {
		t.Error("obsolete fill published under replacement object metadata")
	}
	if store.HasBlock(0) {
		t.Error("obsolete fill created a completed replacement block")
	}
	if got, err := os.ReadFile(store.tmpPath(0)); err != nil || string(got) != "NEW-BODY" {
		t.Errorf("obsolete fill changed replacement tmp: bytes=%q err=%v", got, err)
	}
}

func TestBlockFillClaimRejectsAnotherObjectsMetadata(t *testing.T) {
	s := New(Options{BasePath: t.TempDir(), BlockSize: 8})
	waitFillsOnCleanup(t, s)
	store := NewBlockStore(filepath.Join(t.TempDir(), "asset.mkv"), 8)
	if _, err := store.prepareMetaFixture("h", ".mkv", "new", 8); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "OLD-BODY")
	}))
	defer upstream.Close()
	fill, err := s.claimBlockFill(context.Background(), store, blockSpan{Idx: 0, From: 0, To: 7}, 8, fillSource{object: "old", target: upstreamTarget{url: upstream.URL, client: upstream.Client()}})
	if fill != nil || err == nil {
		t.Fatalf("stale claim started fill=%v err=%v", fill != nil, err)
	}
}

func TestMemoryFallbackDoesNotReadAnotherObjectsDiskBlock(t *testing.T) {
	s := New(Options{BasePath: t.TempDir(), BlockSize: 8})
	waitFillsOnCleanup(t, s)
	store := NewBlockStore(filepath.Join(t.TempDir(), "asset.mkv"), 8)
	if _, err := store.prepareMetaFixture("h", ".mkv", "old", 8); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteBlock(0, []byte("OLD-BODY")); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusPartialContent)
		_, _ = io.WriteString(w, "NEW-BODY")
	}))
	defer upstream.Close()
	var got bytes.Buffer
	err := s.serveBlockSpan(context.Background(), upstream.Client(), &got, store, blockSpan{Idx: 0, From: 0, To: 7}, 8, upstream.URL, "", admission.CacheMemoryOnly, false, nil, nil, func() fillSource {
		return fillSource{object: "new", target: upstreamTarget{url: upstream.URL, client: upstream.Client()}}
	})
	if err != nil || got.String() != "NEW-BODY" {
		t.Fatalf("memory fallback read=%q err=%v, want new object", got.String(), err)
	}
}

func TestBlockFillReaderRejectsReusedPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "block.tmp")
	if err := os.WriteFile(path, []byte("OLD-BODY"), 0o644); err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer original.Close()
	info, err := original.Stat()
	if err != nil {
		t.Fatal(err)
	}
	fill := &blockFill{tmpPath: path, blockPath: path + ".complete", diskInfo: info, written: 8, done: true, changed: make(chan struct{})}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("NEW-BODY"), 0o644); err != nil {
		t.Fatal(err)
	}
	var got bytes.Buffer
	if _, err := readBlockFill(context.Background(), &got, fill, blockSpan{From: 0, To: 7}); err == nil || got.Len() != 0 {
		t.Fatalf("pinned fill followed replacement path: bytes=%q err=%v", got.String(), err)
	}
}
