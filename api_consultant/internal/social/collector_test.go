package social

import (
	"testing"
	"time"
)

func TestEventCollectorNotifiesOnPush(t *testing.T) {
	collector := NewEventCollector()
	collector.Push(EventSignal{ContentType: ContentKnowledge, Headline: "new page", Score: 0.5})

	select {
	case <-collector.Notify():
	case <-time.After(time.Second):
		t.Fatal("expected collector notification")
	}

	events := collector.Drain()
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Headline != "new page" {
		t.Fatalf("unexpected event headline %q", events[0].Headline)
	}
}

func TestEventCollectorCoalescesNotifications(t *testing.T) {
	collector := NewEventCollector()
	collector.Push(EventSignal{ContentType: ContentKnowledge, Headline: "a"})
	collector.Push(EventSignal{ContentType: ContentKnowledge, Headline: "b"})

	select {
	case <-collector.Notify():
	case <-time.After(time.Second):
		t.Fatal("expected collector notification")
	}

	select {
	case <-collector.Notify():
		t.Fatal("expected burst notifications to coalesce")
	default:
	}

	events := collector.Drain()
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
}
