package signalman

import (
	"errors"
	"sync"

	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
)

// SubscriberQueueDepth bounds the events a local subscriber may leave undelivered.
const SubscriberQueueDepth = 256

var (
	// ErrSlowSubscriber ends a subscriber whose queue filled. The upstream
	// stream and every other subscriber keep running.
	ErrSlowSubscriber = errors.New("signalman subscriber fell behind")
	// ErrFanoutClosed ends the subscribers of a fanout that was closed.
	ErrFanoutClosed = errors.New("signalman subscription closed")
)

// Fanout delivers the events of one upstream Signalman stream to independently
// buffered local subscribers. Publishing never blocks: a subscriber that falls
// behind is ended on its own.
type Fanout struct {
	mu     sync.Mutex
	depth  int
	subs   map[*Subscriber]struct{}
	closed error
}

// Subscriber receives events from a Fanout until Done is closed.
type Subscriber struct {
	events chan *signalmanpb.SignalmanEvent
	done   chan struct{}
	err    error
}

// NewFanout returns a Fanout whose subscribers buffer up to depth events.
func NewFanout(depth int) *Fanout {
	if depth < 1 {
		depth = 1
	}
	return &Fanout{depth: depth, subs: make(map[*Subscriber]struct{})}
}

// Attach adds a subscriber. It fails once the fanout is closed.
func (f *Fanout) Attach() (*Subscriber, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed != nil {
		return nil, f.closed
	}
	s := &Subscriber{
		events: make(chan *signalmanpb.SignalmanEvent, f.depth),
		done:   make(chan struct{}),
	}
	f.subs[s] = struct{}{}
	return s, nil
}

// Detach removes s, ends it without an error, and returns how many subscribers
// remain attached. Detaching a subscriber that already ended is a no-op.
func (f *Fanout) Detach(s *Subscriber) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.subs[s]; ok {
		delete(f.subs, s)
		s.end(nil)
	}
	return len(f.subs)
}

// Publish offers event to every subscriber and returns how many were ended
// because their queue was full.
func (f *Fanout) Publish(event *signalmanpb.SignalmanEvent) int {
	if event == nil {
		return 0
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	slow := 0
	for s := range f.subs {
		select {
		case s.events <- event:
		default:
			delete(f.subs, s)
			s.end(ErrSlowSubscriber)
			slow++
		}
	}
	return slow
}

// Close ends every subscriber with err (ErrFanoutClosed when nil) and rejects
// later attaches.
func (f *Fanout) Close(err error) {
	if err == nil {
		err = ErrFanoutClosed
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed != nil {
		return
	}
	f.closed = err
	for s := range f.subs {
		delete(f.subs, s)
		s.end(err)
	}
}

// Len returns the number of attached subscribers.
func (f *Fanout) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs)
}

// end records why the subscriber ended and closes Done. The fanout calls it
// exactly once, under its lock, when removing the subscriber.
func (s *Subscriber) end(err error) {
	s.err = err
	close(s.done)
}

// Events returns the subscriber's buffered event queue.
func (s *Subscriber) Events() <-chan *signalmanpb.SignalmanEvent {
	return s.events
}

// Done is closed when the subscriber has been detached, fell behind, or its
// fanout closed.
func (s *Subscriber) Done() <-chan struct{} {
	return s.done
}

// Err reports why the subscriber ended: nil after Detach, ErrSlowSubscriber, or
// the fanout's close error. Read it only after Done is closed.
func (s *Subscriber) Err() error {
	return s.err
}
