package control

import "sync"

// sourceChangeObservers fans source-entry change notifications out to
// registered callbacks. Callbacks run on the writer's goroutine with no
// registry lock held, so they must be cheap and must not block; they carry
// only the internal name and read current state themselves.
type sourceChangeObservers struct {
	mu     sync.RWMutex
	nextID uint64
	fns    map[uint64]func(internalName string)
}

// AddSourceChangeObserver registers fn to be told about every settled change
// to a stream's source entry: this replica's own write once the shared store
// accepted it, or a peer replica's write applied from the changelog. The
// returned function removes the observer.
func (r *StreamRegistry) AddSourceChangeObserver(fn func(internalName string)) (remove func()) {
	o := &r.sourceObservers
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

func (r *StreamRegistry) notifySourceChanged(internalName string) {
	if internalName == "" {
		return
	}
	o := &r.sourceObservers
	o.mu.RLock()
	defer o.mu.RUnlock()
	for _, fn := range o.fns {
		fn(internalName)
	}
}

// CachedSource returns this replica's in-memory source entry for internalName
// without hydration or a shared-store read: the same view Snapshot serves.
func (r *StreamRegistry) CachedSource(internalName string) (StreamEntry, bool) {
	if r == nil || internalName == "" {
		return StreamEntry{}, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	ce := r.byInt[internalName]
	if ce == nil {
		return StreamEntry{}, false
	}
	entry := ce.entry
	entry.Locations = cloneLocations(entry.Locations)
	return entry, true
}
