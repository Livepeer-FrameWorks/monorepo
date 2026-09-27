package storage

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// OldestPending returns when the oldest undelivered entry of lane was written, and false when the
// lane is empty. The time comes from the entry's file name: the Helmsman receive time of an
// appended trigger, or the seal time of a billing window.
func (w *TriggerWAL) OldestPending(lane TriggerLane) (time.Time, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	l := &w.lanes[lane]
	for i := l.head; i < len(l.pending); i++ {
		path := l.pending[i]
		if _, ok := w.active[path]; !ok {
			continue
		}
		name := filepath.Base(path)
		separator := strings.IndexByte(name, '-')
		if separator <= 0 {
			return time.Time{}, false
		}
		millis, err := strconv.ParseInt(name[:separator], 10, 64)
		if err != nil {
			return time.Time{}, false
		}
		return time.UnixMilli(millis), true
	}
	return time.Time{}, false
}
