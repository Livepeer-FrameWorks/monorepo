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
