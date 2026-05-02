package cache

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestNewLRU(t *testing.T) {
	c := NewLRU(1024, 5*time.Minute)
	if c.Len() != 0 {
		t.Fatalf("expected 0, got %d", c.Len())
	}
	if c.SizeBytes() != 0 {
		t.Fatalf("expected 0, got %d", c.SizeBytes())
	}
}

func TestPutGet_HappyPath(t *testing.T) {
	c := NewLRU(1024, 5*time.Minute)
	c.Put("k1", []byte("hello"), "text/plain")

	data, ct, ok := c.Get("k1")
	if !ok {
		t.Fatal("expected hit")
	}
	if string(data) != "hello" {
		t.Fatalf("got %q, want %q", data, "hello")
	}
	if ct != "text/plain" {
		t.Fatalf("got content type %q, want %q", ct, "text/plain")
	}
}

func TestGet_Miss(t *testing.T) {
	c := NewLRU(1024, 5*time.Minute)
	data, ct, ok := c.Get("nonexistent")
	if ok {
		t.Fatal("expected miss")
	}
	if data != nil {
		t.Fatalf("expected nil data, got %v", data)
	}
	if ct != "" {
		t.Fatalf("expected empty content type, got %q", ct)
	}
}

func TestTTL_Expired(t *testing.T) {
	c := NewLRU(1024, 1*time.Millisecond)
	c.Put("k1", []byte("data"), "application/octet-stream")

	time.Sleep(5 * time.Millisecond)

	_, _, ok := c.Get("k1")
	if ok {
		t.Fatal("expected miss after TTL expiry")
	}
	if c.Len() != 0 {
		t.Fatalf("expected expired entry removed, len=%d", c.Len())
	}
}

func TestTTL_NotExpired(t *testing.T) {
	c := NewLRU(1024, 1*time.Hour)
	c.Put("k1", []byte("data"), "text/plain")

	_, _, ok := c.Get("k1")
	if !ok {
		t.Fatal("expected hit before TTL")
	}
}

func TestGetFresh_UsesCallerMaxAge(t *testing.T) {
	c := NewLRU(1024, 1*time.Hour)
	c.Put("k1", []byte("data"), "text/plain")

	time.Sleep(5 * time.Millisecond)

	_, _, ok := c.GetFresh("k1", 1*time.Millisecond)
	if ok {
		t.Fatal("expected miss after caller max age expiry")
	}
	if c.Len() != 0 {
		t.Fatalf("expected expired entry removed, len=%d", c.Len())
	}
}

func TestEviction_MaxBytes(t *testing.T) {
	c := NewLRU(10, 5*time.Minute)
	c.Put("k1", []byte("12345"), "t")
	c.Put("k2", []byte("12345"), "t")

	if c.Len() != 2 || c.SizeBytes() != 10 {
		t.Fatalf("expected 2 items / 10 bytes, got %d / %d", c.Len(), c.SizeBytes())
	}

	// Adding 6 bytes should evict k1 (oldest) to make room
	c.Put("k3", []byte("123456"), "t")

	_, _, ok := c.Get("k1")
	if ok {
		t.Fatal("k1 should have been evicted")
	}
	// k2 may also be evicted since 5+6=11 > 10
	if c.SizeBytes() > 10 {
		t.Fatalf("size %d exceeds max 10", c.SizeBytes())
	}
}

func TestEviction_EvictsLRU(t *testing.T) {
	c := NewLRU(15, 5*time.Minute)
	c.Put("k1", []byte("aaaaa"), "t") // 5 bytes
	c.Put("k2", []byte("bbbbb"), "t") // 5 bytes
	c.Put("k3", []byte("ccccc"), "t") // 5 bytes = 15 total

	// Access k1 to make it recently used
	c.Get("k1")

	// Adding 6 bytes needs 6 free; k2 is LRU (k1 was just accessed, k3 is newer)
	c.Put("k4", []byte("dddddd"), "t")

	if _, _, ok := c.Get("k1"); !ok {
		t.Fatal("k1 should still be present (recently accessed)")
	}
	if _, _, ok := c.Get("k2"); ok {
		t.Fatal("k2 should have been evicted (LRU)")
	}
}

func TestPut_UpdatesExisting(t *testing.T) {
	c := NewLRU(1024, 5*time.Minute)
	c.Put("k1", []byte("old"), "text/plain")
	c.Put("k1", []byte("new-data"), "application/json")

	data, ct, ok := c.Get("k1")
	if !ok {
		t.Fatal("expected hit")
	}
	if string(data) != "new-data" {
		t.Fatalf("got %q, want %q", data, "new-data")
	}
	if ct != "application/json" {
		t.Fatalf("got %q, want %q", ct, "application/json")
	}
	if c.Len() != 1 {
		t.Fatalf("expected 1 entry after update, got %d", c.Len())
	}
	if c.SizeBytes() != 8 {
		t.Fatalf("expected 8 bytes, got %d", c.SizeBytes())
	}
}

func TestDelete(t *testing.T) {
	c := NewLRU(1024, 5*time.Minute)
	c.Put("k1", []byte("data"), "text/plain")

	if !c.Delete("k1") {
		t.Fatal("expected delete hit")
	}
	if c.Delete("k1") {
		t.Fatal("expected second delete miss")
	}
	if _, _, ok := c.Get("k1"); ok {
		t.Fatal("deleted key should miss")
	}
	if c.Len() != 0 || c.SizeBytes() != 0 {
		t.Fatalf("expected empty cache, len=%d size=%d", c.Len(), c.SizeBytes())
	}
}

func TestLen(t *testing.T) {
	c := NewLRU(1024, 5*time.Minute)
	for i := range 5 {
		c.Put(fmt.Sprintf("k%d", i), []byte("x"), "t")
	}
	if c.Len() != 5 {
		t.Fatalf("expected 5, got %d", c.Len())
	}
}

func TestSizeBytes(t *testing.T) {
	c := NewLRU(1024, 5*time.Minute)
	c.Put("k1", []byte("abc"), "t")   // 3 bytes
	c.Put("k2", []byte("defgh"), "t") // 5 bytes
	if c.SizeBytes() != 8 {
		t.Fatalf("expected 8 bytes, got %d", c.SizeBytes())
	}
}

func TestSizeBytes_AfterEviction(t *testing.T) {
	c := NewLRU(8, 5*time.Minute)
	c.Put("k1", []byte("abc"), "t")   // 3
	c.Put("k2", []byte("defgh"), "t") // 5 = 8 total
	c.Put("k3", []byte("ij"), "t")    // 2 + evicts k1 (3) → 5+2=7

	if c.SizeBytes() > 8 {
		t.Fatalf("size %d exceeds max 8", c.SizeBytes())
	}
}

func TestConcurrent_ReadWrite(t *testing.T) {
	c := NewLRU(4096, 5*time.Minute)
	var wg sync.WaitGroup

	for i := range 50 {
		wg.Add(2)
		key := fmt.Sprintf("key-%d", i)
		go func() {
			defer wg.Done()
			c.Put(key, []byte("data"), "t")
		}()
		go func() {
			defer wg.Done()
			c.Get(key)
		}()
	}
	wg.Wait()

	if c.Len() < 0 {
		t.Fatal("negative length after concurrent access")
	}
	if c.SizeBytes() < 0 {
		t.Fatal("negative size after concurrent access")
	}
}

func TestEmptyData(t *testing.T) {
	c := NewLRU(1024, 5*time.Minute)
	c.Put("empty", []byte{}, "t")

	data, _, ok := c.Get("empty")
	if !ok {
		t.Fatal("expected hit for empty data")
	}
	if len(data) != 0 {
		t.Fatalf("expected empty data, got %d bytes", len(data))
	}
	if c.SizeBytes() != 0 {
		t.Fatalf("expected 0 bytes for empty data, got %d", c.SizeBytes())
	}
}
