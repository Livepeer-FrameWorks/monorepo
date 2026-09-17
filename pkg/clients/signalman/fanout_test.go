package signalman

import (
	"errors"
	"sync"
	"testing"
	"time"

	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
)

func testEvent(eventType signalmanpb.EventType) *signalmanpb.SignalmanEvent {
	return &signalmanpb.SignalmanEvent{EventType: eventType}
}

func receive(t *testing.T, s *Subscriber) *signalmanpb.SignalmanEvent {
	t.Helper()
	select {
	case event := <-s.Events():
		return event
	case <-time.After(time.Second):
		t.Fatal("subscriber did not receive an event")
		return nil
	}
}

func mustAttach(t *testing.T, f *Fanout) *Subscriber {
	t.Helper()
	s, err := f.Attach()
	if err != nil {
		t.Fatalf("Attach: %v", err)
	}
	return s
}

func isDone(s *Subscriber) bool {
	select {
	case <-s.Done():
		return true
	default:
		return false
	}
}

func TestFanoutDeliversEveryEventToEverySubscriber(t *testing.T) {
	f := NewFanout(8)
	subs := []*Subscriber{mustAttach(t, f), mustAttach(t, f), mustAttach(t, f)}
	events := []*signalmanpb.SignalmanEvent{
		testEvent(signalmanpb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE),
		testEvent(signalmanpb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE),
		testEvent(signalmanpb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE),
	}
	for _, event := range events {
		if slow := f.Publish(event); slow != 0 {
			t.Fatalf("Publish ended %d subscribers, want 0", slow)
		}
	}
	for i, s := range subs {
		for _, want := range events {
			if got := receive(t, s); got != want {
				t.Fatalf("subscriber %d got %v, want %v", i, got.GetEventType(), want.GetEventType())
			}
		}
	}
}

func TestFanoutEndsOnlyTheSlowSubscriber(t *testing.T) {
	f := NewFanout(2)
	slow := mustAttach(t, f)
	fast := mustAttach(t, f)

	f.Publish(testEvent(signalmanpb.EventType_EVENT_TYPE_STREAM_END))
	f.Publish(testEvent(signalmanpb.EventType_EVENT_TYPE_STREAM_END))
	receive(t, fast)
	receive(t, fast)

	third := testEvent(signalmanpb.EventType_EVENT_TYPE_STREAM_BUFFER)
	if ended := f.Publish(third); ended != 1 {
		t.Fatalf("Publish ended %d subscribers, want the one full queue", ended)
	}
	if !isDone(slow) || !errors.Is(slow.Err(), ErrSlowSubscriber) {
		t.Fatalf("slow subscriber done=%v err=%v, want ErrSlowSubscriber", isDone(slow), slow.Err())
	}
	if isDone(fast) {
		t.Fatal("a slow sibling ended the fast subscriber")
	}
	if got := receive(t, fast); got != third {
		t.Fatalf("fast subscriber got %v after sibling ended, want third event", got.GetEventType())
	}
	if f.Len() != 1 {
		t.Fatalf("Len = %d after slow subscriber ended, want 1", f.Len())
	}
}

func TestFanoutDetachEndsCleanlyAndReportsRemaining(t *testing.T) {
	f := NewFanout(4)
	a := mustAttach(t, f)
	b := mustAttach(t, f)

	if remaining := f.Detach(a); remaining != 1 {
		t.Fatalf("Detach remaining = %d, want 1", remaining)
	}
	if !isDone(a) || a.Err() != nil {
		t.Fatalf("detached subscriber done=%v err=%v, want done with nil error", isDone(a), a.Err())
	}
	if remaining := f.Detach(a); remaining != 1 {
		t.Fatalf("second Detach remaining = %d, want no-op 1", remaining)
	}

	f.Publish(testEvent(signalmanpb.EventType_EVENT_TYPE_STREAM_END))
	select {
	case <-a.Events():
		t.Fatal("detached subscriber received an event")
	default:
	}
	receive(t, b)
}

func TestFanoutCloseEndsAllAndRejectsAttach(t *testing.T) {
	f := NewFanout(4)
	a := mustAttach(t, f)
	b := mustAttach(t, f)
	shutdown := errors.New("gateway shutting down")

	f.Close(shutdown)
	for _, s := range []*Subscriber{a, b} {
		if !isDone(s) || !errors.Is(s.Err(), shutdown) {
			t.Fatalf("subscriber done=%v err=%v after Close, want shutdown error", isDone(s), s.Err())
		}
	}
	if _, err := f.Attach(); !errors.Is(err, shutdown) {
		t.Fatalf("Attach after Close err = %v, want shutdown error", err)
	}
	f.Close(nil)
	if f.Len() != 0 {
		t.Fatalf("Len after Close = %d", f.Len())
	}
}

func TestFanoutConcurrentPublishAttachDetach(t *testing.T) {
	f := NewFanout(SubscriberQueueDepth)
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				f.Publish(testEvent(signalmanpb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE))
			}
		}
	}()
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				s, err := f.Attach()
				if err != nil {
					return
				}
				select {
				case <-s.Events():
				case <-s.Done():
				case <-time.After(10 * time.Millisecond):
				}
				f.Detach(s)
			}
		}()
	}

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()
	f.Close(nil)
}
