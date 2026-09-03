package leases

import "time"

// This file holds introspection/setup helpers used only by the leases package's
// own tests. Keeping them out of the production build means no non-test code can
// install a lease without its registry entry or read tracker internals outside
// the reconcile/acquire contract.

// acquireSourceForTest installs (or refreshes) a SourceLease directly, without a
// registry entry — a low-level setup shim replacing the removed production
// AcquireSource. Refreshing an existing streamName only bumps liveness.
func (t *Tracker) acquireSourceForTest(streamName string, localPaths []string, key AssetKey, segmentNames []string, degraded bool) {
	if t == nil || streamName == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if existing, ok := t.sources[streamName]; ok {
		existing.LastSeen = time.Now()
		existing.missingSince = time.Time{}
		return
	}
	t.installSourceLocked(streamName, localPaths, key, segmentNames, degraded)
}

// HasSourceLease reports whether streamName currently has a SourceLease.
func (t *Tracker) HasSourceLease(streamName string) bool {
	if t == nil || streamName == "" {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.sources[streamName]
	return ok
}

// IsSourceDegraded reports whether streamName has a SourceLease in the degraded
// (unresolved, type-only) state.
func (t *Tracker) IsSourceDegraded(streamName string) bool {
	if t == nil || streamName == "" {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	lease, ok := t.sources[streamName]
	return ok && lease.Degraded
}

// sourcePresence reports lease presence and registry presence for streamName
// under a SINGLE tracker lock, so a test can assert the two never disagree
// mid-flight (every mutation sets or clears both under this lock).
func (t *Tracker) sourcePresence(streamName string) (leasePresent, registryPresent bool) {
	if t == nil || streamName == "" {
		return false, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, leasePresent = t.sources[streamName]
	if t.registry != nil {
		_, registryPresent = t.registry.Lookup(streamName)
	}
	return leasePresent, registryPresent
}
