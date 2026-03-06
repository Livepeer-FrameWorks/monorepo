package cache

import (
	"container/list"
	"sync"
	"time"
)

type entry struct {
	key         string
	data        []byte
	contentType string
	fetchedAt   time.Time
}

// LRU is a thread-safe, size-bounded, TTL-aware cache for S3 objects.
type LRU struct {
	mu       sync.RWMutex
	maxBytes int64
	ttl      time.Duration
	curBytes int64
	items    map[string]*list.Element
	order    *list.List
}

func NewLRU(maxBytes int64, ttl time.Duration) *LRU {
	return &LRU{
		maxBytes: maxBytes,
		ttl:      ttl,
		items:    make(map[string]*list.Element),
		order:    list.New(),
	}
}

func (c *LRU) Get(key string) ([]byte, string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	elem, ok := c.items[key]
	if !ok {
		return nil, "", false
	}
	e := elem.Value.(*entry)
	if time.Since(e.fetchedAt) > c.ttl {
		c.removeElement(elem)
		return nil, "", false
	}
	c.order.MoveToFront(elem)
	return e.data, e.contentType, true
}

func (c *LRU) Put(key string, data []byte, contentType string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if elem, ok := c.items[key]; ok {
		c.removeElement(elem)
	}

	for c.curBytes+int64(len(data)) > c.maxBytes && c.order.Len() > 0 {
		c.removeElement(c.order.Back())
	}

	e := &entry{
		key:         key,
		data:        data,
		contentType: contentType,
		fetchedAt:   time.Now(),
	}
	elem := c.order.PushFront(e)
	c.items[key] = elem
	c.curBytes += int64(len(data))
}

func (c *LRU) removeElement(elem *list.Element) {
	e := elem.Value.(*entry)
	c.order.Remove(elem)
	delete(c.items, e.key)
	c.curBytes -= int64(len(e.data))
}

func (c *LRU) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.order.Len()
}

func (c *LRU) SizeBytes() int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.curBytes
}
