package provisioner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// The error remotedev's relayout engine contract died on: ysqlsh reports the read restart without its SQLSTATE.
var errStagingReadRestart = errors.New(`fw-yugabyte-contract-665077-665729: ssh fw-yugabyte-contract-665077-665729: "exec -i fw-yugabyte-contract-665077-665729 sh -s" exited 3: ysqlsh:/tmp/tmp.4F6p6RvM8x:5: ERROR:  Restart read required: exit status 3`)

func instantRelayoutRetries(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	previous := relayoutSleep
	relayoutSleep = func(_ context.Context, d time.Duration) error {
		waits = append(waits, d)
		return nil
	}
	t.Cleanup(func() { relayoutSleep = previous })
	return &waits
}

func replyError(err error) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return "", err }
}

func TestQueryIdempotentRetriesReadRestart(t *testing.T) {
	waits := instantRelayoutRetries(t)
	node := &scriptedRelayoutNode{replies: []func(context.Context) (string, error){
		replyError(errStagingReadRestart),
		func(context.Context) (string, error) { return "TRUNCATE public.a", nil },
	}}

	out, err := queryIdempotent(context.Background(), node, "purser__relayout", "SELECT 1")
	if err != nil {
		t.Fatalf("a read restart must be retried, got %v", err)
	}
	if out != "TRUNCATE public.a" || node.calls != 2 {
		t.Fatalf("out=%q calls=%d, want the second attempt's output after 2 calls", out, node.calls)
	}
	if len(*waits) != 1 || (*waits)[0] != relayoutTransientBackoff[0] {
		t.Fatalf("waits = %v, want one wait of %v", *waits, relayoutTransientBackoff[0])
	}
}

func TestQueryIdempotentFailsImmediatelyOnOtherErrors(t *testing.T) {
	waits := instantRelayoutRetries(t)
	permanent := errors.New(`ysqlsh: ERROR:  permission denied for table ledger`)
	node := &scriptedRelayoutNode{replies: []func(context.Context) (string, error){replyError(permanent)}}

	_, err := queryIdempotent(context.Background(), node, "purser", "SELECT 1")
	if !errors.Is(err, permanent) || node.calls != 1 || len(*waits) != 0 {
		t.Fatalf("err=%v calls=%d waits=%v, want the error after one call and no retry", err, node.calls, *waits)
	}
}

func TestQueryIdempotentGivesUpAfterBoundedRetries(t *testing.T) {
	instantRelayoutRetries(t)
	node := &scriptedRelayoutNode{replies: []func(context.Context) (string, error){replyError(errStagingReadRestart)}}

	_, err := queryIdempotent(context.Background(), node, "purser", "SELECT 1")
	want := len(relayoutTransientBackoff) + 1
	if err == nil || node.calls != want || !strings.Contains(err.Error(), "Restart read required") {
		t.Fatalf("err=%v calls=%d, want the read restart after %d calls", err, node.calls, want)
	}
}

func TestQueryIdempotentStopsWhenContextEnds(t *testing.T) {
	previous := relayoutSleep
	relayoutSleep = func(ctx context.Context, _ time.Duration) error { return context.Canceled }
	t.Cleanup(func() { relayoutSleep = previous })
	node := &scriptedRelayoutNode{replies: []func(context.Context) (string, error){replyError(errStagingReadRestart)}}

	_, err := queryIdempotent(context.Background(), node, "purser", "SELECT 1")
	if !errors.Is(err, context.Canceled) || node.calls != 1 {
		t.Fatalf("err=%v calls=%d, want cancellation after one call", err, node.calls)
	}
}

func TestIsRelayoutTransient(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{errStagingReadRestart, true},
		{errors.New("ERROR:  could not serialize access due to concurrent update"), true},
		{errors.New("ERROR:  Operation failed. Try again: tablet leader changed"), true},
		{errors.New("ERROR:  permission denied for table ledger"), false},
		{errors.New("ssh: connection reset"), false},
		{nil, false},
	}
	for _, tc := range cases {
		if got := isRelayoutTransient(tc.err); got != tc.want {
			t.Errorf("isRelayoutTransient(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}
