package main

import (
	"testing"
	"time"
)

func TestEventIDWindowSuppressesDuplicatesWithinTTL(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	w := newEventIDWindow(4, time.Minute)
	w.now = func() time.Time { return now }

	if w.seen("evt-1") {
		t.Fatal("first delivery reported as duplicate")
	}
	if !w.seen("evt-1") {
		t.Fatal("mirrored redelivery within TTL was not suppressed")
	}
	for range 2 {
		if w.seen("") {
			t.Fatal("empty event IDs must never be suppressed")
		}
	}

	now = now.Add(time.Minute)
	if w.seen("evt-1") {
		t.Fatal("delivery after TTL expiry reported as duplicate")
	}
	if !w.seen("evt-1") {
		t.Fatal("re-recorded ID was not suppressed on the next delivery")
	}
}

func TestEventIDWindowEvictsOldestAtCapacity(t *testing.T) {
	w := newEventIDWindow(2, time.Hour)
	for _, id := range []string{"a", "b", "c"} {
		if w.seen(id) {
			t.Fatalf("first delivery of %s reported as duplicate", id)
		}
	}
	if w.seen("a") {
		t.Fatal("evicted ID a still suppressed")
	}
	if !w.seen("c") {
		t.Fatal("retained ID c not suppressed")
	}
}

func TestEventIDWindowReRecordDoesNotLoseNewEntryOnSlotReuse(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	w := newEventIDWindow(3, time.Minute)
	w.now = func() time.Time { return now }

	w.seen("x") // slot 0
	now = now.Add(2 * time.Minute)
	w.seen("x") // expired: re-recorded in slot 1, slot 0 cleared
	w.seen("y") // slot 2
	w.seen("z") // wraps to slot 0, which no longer names x

	if !w.seen("x") {
		t.Fatal("slot reuse evicted the re-recorded entry for x")
	}
}
