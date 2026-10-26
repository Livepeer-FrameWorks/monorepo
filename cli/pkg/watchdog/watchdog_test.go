package watchdog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Work that keeps reporting runs far past its stall window.
func TestReportedProgressKeepsContextAlive(t *testing.T) {
	ctx, w := Start(context.Background(), "test work", 40*time.Millisecond)
	defer w.Stop()
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		Report(ctx, "step")
		time.Sleep(5 * time.Millisecond)
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("context cancelled while progress kept arriving: %v (cause %v)", err, context.Cause(ctx))
	}
}

// A stall cancels the context with a StallError naming the operation and the last progress.
func TestStallCancelsWithLastProgress(t *testing.T) {
	ctx, w := Start(context.Background(), "yugabyte migrations", 30*time.Millisecond)
	defer w.Stop()
	Report(ctx, "TASK [Apply migration (Yugabyte)] item=periscope/033")
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not cancel a stalled context")
	}
	var stall *StallError
	if !errors.As(context.Cause(ctx), &stall) {
		t.Fatalf("cause = %v, want *StallError", context.Cause(ctx))
	}
	msg := w.Err(fmt.Errorf("run playbook: %w", ctx.Err())).Error()
	for _, want := range []string{"yugabyte migrations stalled", "no progress for 30ms", "periscope/033", "context canceled"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("stall error %q does not contain %q", msg, want)
		}
	}
}

// Without a stall, Err leaves errors unchanged.
func TestErrPassesThroughWithoutStall(t *testing.T) {
	_, w := Start(context.Background(), "work", time.Hour)
	defer w.Stop()
	orig := errors.New("boom")
	if got := w.Err(orig); got != orig { //nolint:errorlint // Err must return the very same error value without a stall
		t.Fatalf("Err = %v, want the original error", got)
	}
	if w.Err(nil) != nil {
		t.Fatal("Err(nil) != nil")
	}
}

// Writer reports each completed line; a partial line is not progress until it completes.
func TestWriterReportsCompletedLines(t *testing.T) {
	ctx, w := Start(context.Background(), "playbook", time.Hour)
	defer w.Stop()
	out := WriterFrom(ctx)
	if out == nil {
		t.Fatal("WriterFrom returned nil for a watchdog context")
	}
	fmt.Fprint(out, "TASK [one]\nTASK [tw")
	if got, _ := w.LastProgress(); got != "TASK [one]" {
		t.Fatalf("last = %q, want the completed line", got)
	}
	fmt.Fprint(out, "o]\n\n")
	if got, _ := w.LastProgress(); got != "TASK [two]" {
		t.Fatalf("last = %q, want the reassembled line", got)
	}
}

// Progress on a nested watchdog also counts for the outer one.
func TestNestedProgressReachesOuterWatchdog(t *testing.T) {
	outer, ow := Start(context.Background(), "outer", 40*time.Millisecond)
	defer ow.Stop()
	inner, iw := Start(outer, "inner", time.Hour)
	defer iw.Stop()
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		Report(inner, "inner step")
		time.Sleep(5 * time.Millisecond)
	}
	if outer.Err() != nil {
		t.Fatalf("outer watchdog fired while the nested one reported progress: %v", context.Cause(outer))
	}
}

// Without a watchdog in the context, the package helpers are no-ops.
func TestHelpersWithoutWatchdog(t *testing.T) {
	ctx := context.Background()
	Report(ctx, "x")
	Reportf(ctx, "x %d", 1)
	if WriterFrom(ctx) != nil {
		t.Fatal("WriterFrom returned a writer without a watchdog")
	}
}
