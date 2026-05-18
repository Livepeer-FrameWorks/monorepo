package relay

// Streaming primitives for stream-first cold reads. The block cache is
// a side-effect of streaming, not a prerequisite for serving the
// client. First byte to Mist == first byte from S3; disk fill runs on
// a separate goroutine behind a small bounded channel so that slow or
// stuck disk I/O never stalls the response.

import (
	"errors"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

// clampedWriter forwards only the bytes whose absolute position within
// the block falls inside [from, to] (inclusive). Writes outside that
// range are silently dropped. blockOffset tracks how many bytes of the
// block have been written so far.
//
// After every successful forward, the underlying writer is flushed if
// it implements http.Flusher — that's what gets the bytes through Go's
// net/http response buffering and onto the TCP socket. Without the
// flush, byte 0 of a cold range request sits in the response buffer
// until the relay finishes reading the entire block (defeats the
// stream-first goal).
type clampedWriter struct {
	w           io.Writer
	flusher     http.Flusher
	from        int64 // first byte position in the block to forward
	to          int64 // last byte position in the block to forward (inclusive)
	blockOffset int64 // absolute position within the block of the next byte
}

func newClampedWriter(w io.Writer, from, to int64) *clampedWriter {
	cw := &clampedWriter{w: w, from: from, to: to}
	if f, ok := w.(http.Flusher); ok {
		cw.flusher = f
	}
	return cw
}

func (c *clampedWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	chunkStart := c.blockOffset
	chunkEnd := chunkStart + int64(len(p)) - 1
	c.blockOffset = chunkEnd + 1
	// Entirely before requested range or entirely after — accept, drop.
	if chunkEnd < c.from || chunkStart > c.to {
		return len(p), nil
	}
	// Compute the slice of p that falls inside [from, to].
	startInP := int64(0)
	if chunkStart < c.from {
		startInP = c.from - chunkStart
	}
	endInP := int64(len(p))
	if chunkEnd > c.to {
		endInP = c.to - chunkStart + 1
	}
	if _, err := c.w.Write(p[startInP:endInP]); err != nil {
		return 0, err
	}
	if c.flusher != nil {
		c.flusher.Flush()
	}
	return len(p), nil
}

// tolerantTee writes each chunk to primary inline and forwards a copy
// to secondary through a small bounded channel drained by a background
// goroutine. The client (primary) is never delayed by secondary I/O:
//
//   - Primary error → returned to the caller (playback path mustn't fail
//     silently); the secondary worker is then stopped without draining.
//   - Secondary error → secondary flagged dead, future chunks skip the
//     channel and go primary-only.
//   - Secondary channel full → secondary flagged dead immediately (the
//     disk is falling behind faster than we can buffer); the cache fill
//     is abandoned and the stream continues at network speed.
//
// Buffer sizing: a handful of chunks is enough headroom for an SSD that
// briefly hiccups and not enough to matter for memory. Each chunk is
// copied because io.CopyBuffer reuses its internal buffer between
// Write calls.
//
// Close must be called after the last Write to wait for the worker to
// drain any in-flight chunks. After Close, SecondaryAlive reflects
// whether the disk side actually captured every byte — callers that
// rename a tmpfile into a canonical cache path must check it.
type tolerantTee struct {
	primary       io.Writer
	secondary     io.Writer
	onDead        func(err error)
	secondaryDead atomic.Bool

	ch        chan []byte
	workerWG  sync.WaitGroup
	deadOnce  sync.Once
	closeOnce sync.Once
}

// teeChanCapacity bounds the in-flight chunk queue between the network
// reader and the disk writer. Four 256 KiB chunks = ~1 MiB of headroom,
// chosen for SSD micro-stall absorption; falling behind beyond that is
// a signal that the disk can't keep up and the cache should be
// abandoned rather than slowed down further.
const teeChanCapacity = 4

func newTolerantTee(primary, secondary io.Writer, onDead func(err error)) *tolerantTee {
	t := &tolerantTee{
		primary:   primary,
		secondary: secondary,
		onDead:    onDead,
		ch:        make(chan []byte, teeChanCapacity),
	}
	t.workerWG.Add(1)
	go t.diskWorker()
	return t
}

func (t *tolerantTee) diskWorker() {
	defer t.workerWG.Done()
	for chunk := range t.ch {
		if t.secondaryDead.Load() {
			continue
		}
		if _, err := t.secondary.Write(chunk); err != nil {
			t.markDead(err)
		}
	}
}

func (t *tolerantTee) markDead(err error) {
	t.deadOnce.Do(func() {
		t.secondaryDead.Store(true)
		if t.onDead != nil {
			t.onDead(err)
		}
	})
}

func (t *tolerantTee) Write(p []byte) (int, error) {
	n, err := t.primary.Write(p)
	if err != nil {
		return n, err
	}
	if t.secondaryDead.Load() {
		return n, nil
	}
	// Defensive copy: io.CopyBuffer reuses its scratch buffer between
	// Write calls, so the bytes in p are only valid until we return.
	chunk := make([]byte, len(p))
	copy(chunk, p)
	select {
	case t.ch <- chunk:
	default:
		// Disk side is falling behind faster than the channel can buffer.
		// Drop the cache rather than apply backpressure to the client.
		t.markDead(errors.New("tolerantTee: disk side fell behind; cache abandoned"))
	}
	return n, nil
}

// Close stops the disk worker after draining any queued chunks. Safe
// to call multiple times. Returns when the disk side has finished its
// pending writes (or has been marked dead).
func (t *tolerantTee) Close() {
	t.closeOnce.Do(func() { close(t.ch) })
	t.workerWG.Wait()
}

// SecondaryAlive reports whether every chunk so far has been
// successfully forwarded to the secondary. False once any write
// errored, the secondary channel ran full, or Close happened with
// chunks still in flight that errored.
func (t *tolerantTee) SecondaryAlive() bool { return !t.secondaryDead.Load() }
