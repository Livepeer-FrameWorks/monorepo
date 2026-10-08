package logic

import (
	"context"
	"maps"
	"strings"
	"sync"
	"time"

	"frameworks/api_dns/internal/bunnyrecords"
	"frameworks/api_dns/internal/provider/bunny"
)

// minMemberRemovalGrace is the shortest time a published member must stay out
// of the healthy set before Navigator removes it. Health flaps shorter than
// this (an announced Helmsman restart, a Quartermaster read racing a health
// write) never reach DNS.
const minMemberRemovalGrace = 3 * time.Minute

func (m *DNSManager) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

// memberRemovalGrace is max(record TTL, minMemberRemovalGrace): a resolver that
// cached the member just before it left keeps a usable answer for its TTL.
func (m *DNSManager) memberRemovalGrace() time.Duration {
	grace := time.Duration(m.recordTTL) * time.Second
	if grace < minMemberRemovalGrace {
		grace = minMemberRemovalGrace
	}
	return grace
}

// departingWithinGrace reports whether the published member key is still
// inside its removal grace. The first time a member is seen published but not
// desired starts its clock, so after a Navigator restart every departing
// member gets the full grace again.
func (m *DNSManager) departingWithinGrace(key string, now time.Time) bool {
	m.membershipMu.Lock()
	defer m.membershipMu.Unlock()
	if m.departingSince == nil {
		m.departingSince = map[string]time.Time{}
	}
	since, ok := m.departingSince[key]
	if !ok {
		m.departingSince[key] = now
		return true
	}
	return now.Sub(since) < m.memberRemovalGrace()
}

// forgetDepartures drops the departure clocks under scope that this sync did
// not see departing: the member is healthy again or no longer published.
func (m *DNSManager) forgetDepartures(scope string, seen map[string]struct{}) {
	prefix := scope + "|"
	m.membershipMu.Lock()
	defer m.membershipMu.Unlock()
	for key := range m.departingSince {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		if _, ok := seen[key]; !ok {
			delete(m.departingSince, key)
		}
	}
}

// membershipChange summarizes one record set reconcile for the decision log.
type membershipChange struct {
	Retained []string
	Dropped  []string
}

// withDepartingMembers returns desired plus every member of the published
// record set (zone, recordName) that is not desired and has been departing for
// less than the removal grace. desired must be non-empty: an empty healthy set
// never reaches a record set reconcile.
func (m *DNSManager) withDepartingMembers(zoneDomain, recordName, fqdn string, desired, published []bunny.Record) ([]bunny.Record, membershipChange) {
	scope := "set:" + bunnyZoneCacheKey(zoneDomain) + "/" + bunnyRecordNameKey(recordName)
	want := make(map[string]struct{}, len(desired))
	for _, record := range desired {
		want[record.Value] = struct{}{}
	}
	now := m.clock()
	seen := map[string]struct{}{}
	var change membershipChange
	out := desired
	for _, record := range published {
		if record.Type != bunny.RecordTypeA || bunnyRecordNameKey(record.Name) != bunnyRecordNameKey(recordName) {
			continue
		}
		if _, ok := want[record.Value]; ok {
			continue
		}
		key := scope + "|" + record.Value
		seen[key] = struct{}{}
		if !m.departingWithinGrace(key, now) {
			change.Dropped = append(change.Dropped, record.Value)
			continue
		}
		want[record.Value] = struct{}{}
		change.Retained = append(change.Retained, record.Value)
		out = append(out, bunnyrecords.ARecord(bunnyrecords.ARecordInput{
			Name:  recordName,
			Value: record.Value,
			TTL:   m.recordTTL,
			FQDN:  fqdn,
			Geography: bunnyrecords.GeoCoordinates{
				Latitude:  record.GeolocationLatitude,
				Longitude: record.GeolocationLongitude,
			},
		}))
	}
	m.forgetDepartures(scope, seen)
	return out, change
}

func bunnyRecordNameKey(name string) string {
	name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
	if name == "@" {
		return ""
	}
	return name
}

// syncSerializer runs at most one DNS sync per key at a time. Callers waiting
// behind a running sync share the next run: a run that starts after a
// request arrived reads Quartermaster after it and therefore answers it.
type syncSerializer struct {
	mu    sync.Mutex
	slots map[string]*syncSlot
}

type syncSlot struct {
	run       chan struct{} // capacity 1; holding the token is running the sync
	requested uint64        // guarded by syncSerializer.mu
	answered  uint64        // guarded by the run token
	result    map[string]string
}

func (s *syncSerializer) slot(key string) *syncSlot {
	if s.slots == nil {
		s.slots = map[string]*syncSlot{}
	}
	slot := s.slots[key]
	if slot == nil {
		slot = &syncSlot{run: make(chan struct{}, 1)}
		s.slots[key] = slot
	}
	return slot
}

// coalesce runs fn under key unless a run that started after this call
// already completed, in which case it returns that run's partial errors. Only
// a successful run answers waiting callers; after a failed run each waiter
// runs fn itself.
func (s *syncSerializer) coalesce(ctx context.Context, key string, fn func() (map[string]string, error)) (map[string]string, error) {
	s.mu.Lock()
	slot := s.slot(key)
	slot.requested++
	ticket := slot.requested
	s.mu.Unlock()

	select {
	case slot.run <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-slot.run }()

	if slot.answered >= ticket {
		return maps.Clone(slot.result), nil
	}
	s.mu.Lock()
	start := slot.requested
	s.mu.Unlock()
	result, err := fn()
	if err == nil {
		slot.answered = start
		slot.result = maps.Clone(result)
	}
	return result, err
}

// exclusive runs fn under key without answering waiting callers. The polling
// reconcile uses it: its Quartermaster read happened before it took the key,
// so it cannot stand in for a wake that arrived meanwhile.
func (s *syncSerializer) exclusive(ctx context.Context, key string, fn func()) error {
	s.mu.Lock()
	slot := s.slot(key)
	s.mu.Unlock()
	select {
	case slot.run <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-slot.run }()
	fn()
	return nil
}

func clusterSyncKey(clusterID, serviceType string) string {
	return "cluster|" + clusterID + "|" + serviceType
}

func rootSyncKey(serviceType string) string {
	return "root|" + serviceType
}
