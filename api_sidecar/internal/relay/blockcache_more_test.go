package relay

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lastBlockIndex is the floor((totalSize-1)/blockSize). Non-positive sizes
// collapse to block 0; exact multiples must not spill into an extra empty block.
func TestLastBlockIndex(t *testing.T) {
	cases := []struct {
		totalSize, blockSize, want int64
	}{
		{0, 32, 0},
		{-5, 32, 0},
		{1, 32, 0},
		{32, 32, 0},  // exactly one block, indices 0..0
		{33, 32, 1},  // spills into block 1
		{64, 32, 1},  // exactly two blocks
		{65, 32, 2},  // spills into block 2
		{100, 32, 3}, // blocks 0,1,2,3
	}
	for _, c := range cases {
		if got := lastBlockIndex(c.totalSize, c.blockSize); got != c.want {
			t.Errorf("lastBlockIndex(%d,%d)=%d want %d", c.totalSize, c.blockSize, got, c.want)
		}
	}
}

// tmpPath is the in-flight sibling of a completed block file.
func TestTmpPath(t *testing.T) {
	s := NewBlockStore(filepath.Join(t.TempDir(), "vod", "abc.mp4"), 32)
	got := s.tmpPath(2)
	if !strings.HasSuffix(got, ".blk.tmp") {
		t.Fatalf("tmpPath = %q, want a .blk.tmp suffix", got)
	}
	if got != s.BlockPath(2)+".tmp" {
		t.Fatalf("tmpPath = %q, want BlockPath+\".tmp\" = %q", got, s.BlockPath(2)+".tmp")
	}
}

// WriteBlock must land the block atomically: the final .blk exists with the
// exact bytes and no .tmp residue is left behind.
func TestWriteBlock_AtomicAndExact(t *testing.T) {
	s := NewBlockStore(filepath.Join(t.TempDir(), "vod", "abc.mp4"), 8)
	data := []byte("blockdat")
	if err := s.WriteBlock(0, data); err != nil {
		t.Fatalf("WriteBlock: %v", err)
	}
	if !s.HasBlock(0) {
		t.Fatal("HasBlock(0) should be true after WriteBlock")
	}
	got, err := os.ReadFile(s.BlockPath(0))
	if err != nil {
		t.Fatalf("read block: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("block bytes = %q, want %q", got, data)
	}
	if _, err := os.Stat(s.tmpPath(0)); !os.IsNotExist(err) {
		t.Fatal("no .tmp residue should remain after a successful write")
	}
}

// IsComplete is true only when every block in [0,last] is present.
func TestIsComplete(t *testing.T) {
	const blockSize = int64(8)
	s := NewBlockStore(filepath.Join(t.TempDir(), "vod", "abc.mp4"), blockSize)

	if s.IsComplete(0) {
		t.Fatal("totalSize 0 must never be complete")
	}

	// total 20 bytes over blockSize 8 => blocks 0,1,2 (last index 2).
	const total = int64(20)
	for i := int64(0); i <= 2; i++ {
		if err := s.WriteBlock(i, make([]byte, blockSize)); err != nil {
			t.Fatalf("WriteBlock(%d): %v", i, err)
		}
	}
	if !s.IsComplete(total) {
		t.Fatal("all blocks present should report complete")
	}

	// Remove the middle block: no longer complete.
	if err := os.Remove(s.BlockPath(1)); err != nil {
		t.Fatal(err)
	}
	if s.IsComplete(total) {
		t.Fatal("a missing interior block must report incomplete")
	}
}

// CleanTmps sweeps crash-leftover *.blk.tmp without touching real blocks.
func TestCleanTmps(t *testing.T) {
	s := NewBlockStore(filepath.Join(t.TempDir(), "vod", "abc.mp4"), 8)
	if err := s.WriteBlock(0, []byte("realdata")); err != nil {
		t.Fatalf("WriteBlock: %v", err)
	}
	// Simulate an in-flight tmp left by a crash.
	stray := s.tmpPath(1)
	if err := os.WriteFile(stray, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}

	s.CleanTmps()

	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatal("stray .tmp must be swept")
	}
	if !s.HasBlock(0) {
		t.Fatal("completed block must survive CleanTmps")
	}
}

// EnsureMeta keeps the blocks of the object it recorded, drops them for
// another object, and records the first known object over an unknown one
// without dropping anything.
func TestEnsureMetaObjectIdentity(t *testing.T) {
	cases := []struct {
		name         string
		recorded     string
		resolved     string
		keepBlocks   bool
		wantRecorded string
	}{
		{name: "same object", recorded: "https://s3/b/k", resolved: "https://s3/b/k", keepBlocks: true, wantRecorded: "https://s3/b/k"},
		{name: "another object", recorded: "https://s3/b/k", resolved: "https://s3/b/k.att-2", keepBlocks: false, wantRecorded: "https://s3/b/k.att-2"},
		{name: "unknown resolved", recorded: "https://s3/b/k", resolved: "", keepBlocks: true, wantRecorded: "https://s3/b/k"},
		{name: "unknown recorded", recorded: "", resolved: "https://s3/b/k", keepBlocks: true, wantRecorded: "https://s3/b/k"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewBlockStore(filepath.Join(t.TempDir(), "vod", "h.mkv"), 8)
			if _, err := s.EnsureMeta("h", "mkv", tc.recorded, 16); err != nil {
				t.Fatal(err)
			}
			if err := s.WriteBlock(0, []byte("01234567")); err != nil {
				t.Fatal(err)
			}
			m, err := s.EnsureMeta("h", "mkv", tc.resolved, 16)
			if err != nil {
				t.Fatal(err)
			}
			if s.HasBlock(0) != tc.keepBlocks {
				t.Fatalf("block kept=%v, want %v", s.HasBlock(0), tc.keepBlocks)
			}
			if m.Object != tc.wantRecorded {
				t.Fatalf("returned object=%q, want %q", m.Object, tc.wantRecorded)
			}
			saved, err := s.LoadMeta()
			if err != nil || saved.Object != tc.wantRecorded {
				t.Fatalf("saved meta=%+v err=%v, want object %q", saved, err, tc.wantRecorded)
			}
		})
	}
}

// A presigned URL's signature is not part of the object it names; a peer
// relay URL names no object version.
func TestObjectIdentity(t *testing.T) {
	a := &ResolveResult{MediaPresignedURL: "https://s3.example/bucket/vod/t/h/h.mkv.att-1?X-Amz-Signature=aaa&X-Amz-Date=1"}
	b := &ResolveResult{MediaPresignedURL: "https://s3.example/bucket/vod/t/h/h.mkv.att-1?X-Amz-Signature=bbb&X-Amz-Date=2"}
	c := &ResolveResult{MediaPresignedURL: "https://s3.example/bucket/vod/t/h/h.mkv.att-2?X-Amz-Signature=aaa"}
	peer := &ResolveResult{PeerRelayURL: "https://edge-1/internal/artifact/vod/h.mkv", MediaPresignedURL: a.MediaPresignedURL}
	if a.objectIdentity() == "" || a.objectIdentity() != b.objectIdentity() {
		t.Fatalf("re-signed URL changed identity: %q vs %q", a.objectIdentity(), b.objectIdentity())
	}
	if a.objectIdentity() == c.objectIdentity() {
		t.Fatalf("another key kept identity %q", a.objectIdentity())
	}
	if got := peer.objectIdentity(); got != "" {
		t.Fatalf("peer relay identity=%q, want none", got)
	}
}
