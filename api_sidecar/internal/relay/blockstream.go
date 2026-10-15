package relay

// Streaming primitive for memory-only cold reads: the client receives
// the requested slice of a block-aligned upstream response as it
// arrives.

import (
	"io"
	"net/http"
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
