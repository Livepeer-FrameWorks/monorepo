// Package watchdog bounds long CLI operations by progress instead of wall-clock time. A watchdog context is
// cancelled only when no progress is reported for its stall window, so work that keeps reporting runs as long as it
// needs while a hang still ends with an error naming what stalled and the last progress seen.
//
// Progress reaches a watchdog explicitly (Report, or Watchdog.Report) or through Writer, which counts every completed
// output line. ansiblerun feeds every ansible-playbook output line of a run under a watchdog context to it, so a role
// run counts each task, loop item and recap line as progress.
package watchdog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// StallError is the cancellation cause of a watchdog context whose stall window passed without progress.
type StallError struct {
	// What names the operation the watchdog bounds.
	What string
	// Window is the stall window that passed without progress.
	Window time.Duration
	// Started is when the watchdog started.
	Started time.Time
	// Last describes the last progress reported; empty when none was.
	Last string
	// LastAt is when the last progress (or the start) was recorded.
	LastAt time.Time
}

func (e *StallError) Error() string {
	last := "none since start"
	if e.Last != "" {
		last = e.Last
	}
	return fmt.Sprintf("%s stalled: no progress for %s after running %s; last progress: %s",
		e.What, e.Window, e.LastAt.Add(e.Window).Sub(e.Started).Round(time.Second), last)
}

// Watchdog cancels its context when no progress arrives for the stall window.
type Watchdog struct {
	what   string
	stall  time.Duration
	parent *Watchdog
	cancel context.CancelCauseFunc
	done   chan struct{}
	once   sync.Once

	mu      sync.Mutex
	started time.Time
	lastAt  time.Time
	last    string
	stalled *StallError
}

type contextKey struct{}

// Start returns a context derived from parent that is cancelled, with a *StallError cause, once stall passes without
// progress. The context carries the watchdog, so Report and Writer reach it from code that only sees the context;
// progress reported to a nested watchdog also counts for the watchdogs around it. Stop releases the watchdog and
// cancels the context; callers defer it.
func Start(parent context.Context, what string, stall time.Duration) (context.Context, *Watchdog) {
	if stall <= 0 {
		panic("watchdog: stall window must be positive")
	}
	ctx, cancel := context.WithCancelCause(parent)
	now := time.Now()
	w := &Watchdog{
		what:    what,
		stall:   stall,
		parent:  From(parent),
		cancel:  cancel,
		done:    make(chan struct{}),
		started: now,
		lastAt:  now,
	}
	go w.run(ctx)
	return context.WithValue(ctx, contextKey{}, w), w
}

func (w *Watchdog) run(ctx context.Context) {
	timer := time.NewTimer(w.stall)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.done:
			return
		case <-timer.C:
			w.mu.Lock()
			wait := time.Until(w.lastAt.Add(w.stall))
			if wait > 0 {
				w.mu.Unlock()
				timer.Reset(wait)
				continue
			}
			w.stalled = &StallError{What: w.what, Window: w.stall, Started: w.started, Last: w.last, LastAt: w.lastAt}
			stallErr := w.stalled
			w.mu.Unlock()
			w.cancel(stallErr)
			return
		}
	}
}

// Report records progress described by desc and restarts the stall window. It is safe for concurrent use and a no-op
// on a nil watchdog.
func (w *Watchdog) Report(desc string) {
	for cur := w; cur != nil; cur = cur.parent {
		cur.mu.Lock()
		cur.lastAt = time.Now()
		if desc != "" {
			cur.last = desc
		}
		cur.mu.Unlock()
	}
}

// Stop releases the watchdog and cancels its context. It is idempotent.
func (w *Watchdog) Stop() {
	w.once.Do(func() {
		close(w.done)
		w.cancel(context.Canceled)
	})
}

// LastProgress returns the description and time of the last progress, or an empty description and the start time
// when none was reported.
func (w *Watchdog) LastProgress() (string, time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.last, w.lastAt
}

// Stalled returns the stall that cancelled the context, or nil when the watchdog has not fired.
func (w *Watchdog) Stalled() *StallError {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stalled
}

// Err attributes err to the stall when the watchdog fired: the returned error wraps both the *StallError and err, so
// an operation cancelled by the watchdog reports what stalled instead of only "context canceled". Otherwise err is
// returned unchanged.
func (w *Watchdog) Err(err error) error {
	stall := w.Stalled()
	if err == nil || stall == nil {
		return err
	}
	if errors.Is(err, stall) {
		return err
	}
	return fmt.Errorf("%w: %w", stall, err)
}

// Writer returns an io.Writer that reports every completed non-empty line written to it as progress, with the line
// as its description. It is safe for concurrent use; concurrent writers' partial lines are not reassembled.
func (w *Watchdog) Writer() io.Writer {
	return &lineWriter{w: w}
}

// From returns the innermost watchdog ctx carries, or nil.
func From(ctx context.Context) *Watchdog {
	if ctx == nil {
		return nil
	}
	w, _ := ctx.Value(contextKey{}).(*Watchdog) //nolint:errcheck // absent value means no watchdog
	return w
}

// Report records progress on the watchdog ctx carries; it is a no-op when ctx carries none.
func Report(ctx context.Context, desc string) {
	From(ctx).Report(desc)
}

// Reportf is Report with a formatted description.
func Reportf(ctx context.Context, format string, args ...any) {
	if w := From(ctx); w != nil {
		w.Report(fmt.Sprintf(format, args...))
	}
}

// WriterFrom returns the line-progress Writer of the watchdog ctx carries, or nil when it carries none.
func WriterFrom(ctx context.Context) io.Writer {
	if w := From(ctx); w != nil {
		return w.Writer()
	}
	return nil
}

const maxLineDescription = 240

type lineWriter struct {
	w       *Watchdog
	mu      sync.Mutex
	partial []byte
}

func (l *lineWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	data := append(l.partial, p...)
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		if line := string(bytes.TrimSpace(data[:i])); line != "" {
			if len(line) > maxLineDescription {
				line = line[:maxLineDescription] + "..."
			}
			l.w.Report(line)
		}
		data = data[i+1:]
	}
	if len(data) > 64*1024 {
		data = data[len(data)-64*1024:]
	}
	l.partial = append(l.partial[:0], data...)
	return len(p), nil
}
