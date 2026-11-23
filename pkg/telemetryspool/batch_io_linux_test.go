package telemetryspool

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"strings"
	"testing"
)

func TestBatchRetryMetadataWritesAreBoundedAndDurable(t *testing.T) {
	dir := t.TempDir()
	q, err := Open(Config{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 64 {
		if enqueueErr := q.Enqueue(fmt.Sprint(i), []byte("pending")); enqueueErr != nil {
			t.Fatal(enqueueErr)
		}
	}
	fd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(fd)
	if _, watchErr := unix.InotifyAddWatch(fd, dir, unix.IN_CLOSE_WRITE); watchErr != nil {
		t.Fatal(watchErr)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	q.RunBatches(ctx, func() (int, int) { return 64, 1 << 20 }, func(_ context.Context, records [][]byte) []DeliveryOutcome {
		cancel()
		result := make([]DeliveryOutcome, len(records))
		for i := range result {
			result[i].Err = errors.New("receiver unavailable")
		}
		return result
	}, nil)
	writes := 0
	buffer := make([]byte, 64<<10)
	for {
		n, readErr := unix.Read(fd, buffer)
		if errors.Is(readErr, unix.EAGAIN) {
			break
		}
		if readErr != nil {
			t.Fatal(readErr)
		}
		for offset := 0; offset+unix.SizeofInotifyEvent <= n; {
			length := int(binary.NativeEndian.Uint32(buffer[offset+12 : offset+16]))
			end := offset + unix.SizeofInotifyEvent + length
			if end > n {
				t.Fatal("truncated filesystem event")
			}
			if strings.HasPrefix(string(bytes.TrimRight(buffer[offset+unix.SizeofInotifyEvent:end], "\x00")), ".state-") {
				writes++
			}
			offset = end
		}
	}
	if writes > 2 {
		t.Fatalf("one failed upload batch rewrote/fsynced its whole retry index %d times; want at most two", writes)
	}
	reopened, err := Open(Config{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	pending, _, lost := reopened.Stats()
	if pending != 64 || lost != 0 || len(reopened.state.Retries) != 64 {
		t.Fatalf("batch retry lost durable records or retry state: pending=%d lost=%d retries=%d", pending, lost, len(reopened.state.Retries))
	}
}
