package provisioner

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptedRelayoutNode answers admin queries from a script; the last reply repeats once the script runs out.
type scriptedRelayoutNode struct {
	mu      sync.Mutex
	replies []func(ctx context.Context) (string, error)
	calls   int
}

func (n *scriptedRelayoutNode) Name() string { return "scripted" }

func (n *scriptedRelayoutNode) Shell(context.Context, string) (string, error) {
	return "", errors.New("scripted node runs no shell scripts")
}

func (n *scriptedRelayoutNode) Query(ctx context.Context, _, _ string) (string, error) {
	n.mu.Lock()
	i := min(n.calls, len(n.replies)-1)
	n.calls++
	reply := n.replies[i]
	n.mu.Unlock()
	return reply(ctx)
}

func renewed(context.Context) (string, error) { return "purser", nil }

func unreachable(context.Context) (string, error) {
	return "", errors.New("ssh: connection reset")
}

func holdWithScript(replies ...func(ctx context.Context) (string, error)) (*YugabyteRelayout, *scriptedRelayoutNode) {
	node := &scriptedRelayoutNode{replies: replies}
	return &YugabyteRelayout{Primary: node, Database: "purser", LeaseOwner: "unit", LeaseTTL: 1200 * time.Millisecond}, node
}

func TestRelayoutHoldToleratesTransientRenewalFailures(t *testing.T) {
	relayout, _ := holdWithScript(unreachable, unreachable, renewed, unreachable, unreachable, renewed)
	holdCtx, stop := relayout.Hold(context.Background())
	defer stop()
	select {
	case <-time.After(2 * relayout.LeaseTTL):
	case <-holdCtx.Done():
		t.Fatalf("hold cancelled on transient renewal failures: %v", context.Cause(holdCtx))
	}
}

func TestRelayoutHoldCancelsWhenLeaseIsTaken(t *testing.T) {
	relayout, _ := holdWithScript(renewed, func(context.Context) (string, error) { return "", nil })
	holdCtx, stop := relayout.Hold(context.Background())
	defer stop()
	select {
	case <-holdCtx.Done():
	case <-time.After(relayout.LeaseTTL):
		t.Fatal("hold kept running after the journal showed the lease was lost")
	}
	if cause := context.Cause(holdCtx); !errors.Is(cause, errRelayoutLeaseLost) {
		t.Fatalf("cause = %v, want the lost-lease error", cause)
	}
}

func TestRelayoutHoldCancelsBeforeUnrenewedLeaseExpires(t *testing.T) {
	for name, reply := range map[string]func(context.Context) (string, error){
		"unreachable": unreachable,
		"hung": func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	} {
		t.Run(name, func(t *testing.T) {
			relayout, _ := holdWithScript(reply)
			started := time.Now()
			holdCtx, stop := relayout.Hold(context.Background())
			defer stop()
			select {
			case <-holdCtx.Done():
			case <-time.After(3 * relayout.LeaseTTL):
				t.Fatal("hold kept running without a successful renewal")
			}
			elapsed := time.Since(started)
			if elapsed < relayout.LeaseTTL/2 {
				t.Fatalf("hold cancelled after %s, before the lease could have lapsed", elapsed)
			}
			// The lease was taken before Hold started, so it can lapse from one TTL after that; the step must have
			// stopped by then, however long a renewal attempt hangs.
			if elapsed >= relayout.LeaseTTL {
				t.Fatalf("hold cancelled after %s, not before the %s lease could lapse", elapsed, relayout.LeaseTTL)
			}
			if cause := context.Cause(holdCtx); cause == nil || !strings.Contains(cause.Error(), "not renewed") {
				t.Fatalf("cause = %v, want a not-renewed error", cause)
			}
		})
	}
}

// TestRelayoutHoldCancelsBeforeARenewedLeaseLapses renews once and then hangs every later attempt. The renewed lease
// can lapse one TTL after that renewal started, so the step must be cancelled before then.
func TestRelayoutHoldCancelsBeforeARenewedLeaseLapses(t *testing.T) {
	var mu sync.Mutex
	var renewedAt time.Time
	hung := func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	relayout, _ := holdWithScript(func(context.Context) (string, error) {
		mu.Lock()
		renewedAt = time.Now()
		mu.Unlock()
		return "purser", nil
	}, hung)
	holdCtx, stop := relayout.Hold(context.Background())
	defer stop()
	select {
	case <-holdCtx.Done():
	case <-time.After(3 * relayout.LeaseTTL):
		t.Fatal("hold kept running after renewals stopped succeeding")
	}
	cancelled := time.Now()
	mu.Lock()
	defer mu.Unlock()
	if renewedAt.IsZero() {
		t.Fatal("the first renewal never ran")
	}
	if lapse := renewedAt.Add(relayout.LeaseTTL); !cancelled.Before(lapse) {
		t.Fatalf("hold cancelled %s after the renewed lease could lapse", cancelled.Sub(lapse))
	}
}
