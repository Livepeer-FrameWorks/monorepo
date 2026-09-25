package state

import "sync"

// streamChangeObservers fans stream-change notifications out to registered
// callbacks. Callbacks run on the writer's goroutine after sm.mu is released,
// so they must be cheap and must not block; they carry only the stream name
// and read current state themselves.
type streamChangeObservers struct {
	mu     sync.RWMutex
	nextID uint64
	fns    map[uint64]func(internalName string)
}

// AddStreamChangeObserver registers fn to be told about every change to a
// stream's state or to one of its per-node instances, whether this replica
// made the change or applied it from a peer replica's changelog entry. The
// returned function removes the observer.
func (sm *StreamStateManager) AddStreamChangeObserver(fn func(internalName string)) (remove func()) {
	o := &sm.streamObservers
	o.mu.Lock()
	if o.fns == nil {
		o.fns = make(map[uint64]func(string))
	}
	o.nextID++
	id := o.nextID
	o.fns[id] = fn
	o.mu.Unlock()
	return func() {
		o.mu.Lock()
		delete(o.fns, id)
		o.mu.Unlock()
	}
}

func (sm *StreamStateManager) notifyStreamChanged(internalName string) {
	if internalName == "" {
		return
	}
	o := &sm.streamObservers
	o.mu.RLock()
	defer o.mu.RUnlock()
	for _, fn := range o.fns {
		fn(internalName)
	}
}
