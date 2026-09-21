package ledger

import (
	"strings"
	"testing"
	"time"
)

func TestRetryScheduleRunsAboutThreeDays(t *testing.T) {
	var total time.Duration
	for _, d := range RetrySchedule {
		total += d
	}
	if RetrySchedule[0] != 30*time.Second || RetrySchedule[len(RetrySchedule)-1] != 24*time.Hour {
		t.Fatalf("schedule bounds = %s .. %s", RetrySchedule[0], RetrySchedule[len(RetrySchedule)-1])
	}
	if total < 3*24*time.Hour || total > 3*24*time.Hour+12*time.Hour {
		t.Fatalf("schedule totals %s, want about three days", total)
	}
	if MaxAttempts != len(RetrySchedule)+1 {
		t.Fatalf("MaxAttempts = %d", MaxAttempts)
	}
}

func TestRetryDelayJitterAndExhaustion(t *testing.T) {
	low, ok := RetryDelay(1, func() float64 { return 0 })
	high, _ := RetryDelay(1, func() float64 { return 0.999999 })
	if !ok || low != 27*time.Second || high < 32*time.Second || high > 33*time.Second {
		t.Fatalf("jittered first retry = %s .. %s", low, high)
	}
	if _, ok := RetryDelay(MaxAttempts, nil); ok {
		t.Fatal("a retry after the last attempt")
	}
	if _, ok := RetryDelay(0, nil); ok {
		t.Fatal("a retry before any attempt")
	}
}

func TestTruncateExcerpt(t *testing.T) {
	long := strings.Repeat("é", 1000)
	got := truncateExcerpt(long)
	if len(got) > MaxExcerptBytes || len(got) != 1024 {
		t.Fatalf("excerpt is %d bytes", len(got))
	}
	if truncateExcerpt("a\x00b\xffc") != "abc" {
		t.Fatalf("excerpt kept NUL or invalid bytes: %q", truncateExcerpt("a\x00b\xffc"))
	}
}

func TestArrayLiteralQuotes(t *testing.T) {
	if got := arrayLiteral([]string{"stream.live", `a"b\c`, "*"}); got != `{"stream.live","a\"b\\c","*"}` {
		t.Fatalf("literal = %s", got)
	}
	if got := arrayLiteral(nil); got != "{}" {
		t.Fatalf("empty literal = %s", got)
	}
	values, err := jsonStrings(`["a","b"]`)
	if err != nil || len(values) != 2 {
		t.Fatalf("jsonStrings = %v, %v", values, err)
	}
}
