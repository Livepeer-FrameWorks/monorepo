package handlers

import (
	"strings"
	"testing"
)

// ParseProcessExitTrigger decodes Mist's newline-delimited PROCESS_EXIT body.
// The first 7 lines are mandatory; lines 8 and 9 (short/long reason) are
// optional. A body with fewer than 7 lines is a malformed trigger and must
// error rather than silently produce a half-populated event.
func TestParseProcessExitTrigger(t *testing.T) {
	t.Run("too short errors", func(t *testing.T) {
		if _, err := ParseProcessExitTrigger([]byte("a\nb\nc\nd\ne\nf")); err == nil {
			t.Fatal("expected error for <7 lines")
		}
	})

	t.Run("minimal 7 lines", func(t *testing.T) {
		body := strings.Join([]string{"live+demo", "AV", "{}", "4321", "0", "2", "clean"}, "\n")
		evt, err := ParseProcessExitTrigger([]byte(body))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if evt.StreamName != "live+demo" || evt.ProcessType != "AV" || evt.Config != "{}" {
			t.Errorf("header fields wrong: %+v", evt)
		}
		if evt.PID != 4321 || evt.ExitCode != 0 || evt.BootCount != 2 || evt.Status != "clean" {
			t.Errorf("numeric/status fields wrong: %+v", evt)
		}
		if evt.ShortReason != "" || evt.Reason != "" {
			t.Errorf("optional reasons should be empty: %+v", evt)
		}
	})

	t.Run("optional reason lines", func(t *testing.T) {
		body := strings.Join([]string{"s", "Livepeer", "{}", "1", "137", "0", "unrecoverable", "ER_CRASH", "segfault in worker"}, "\n")
		evt, err := ParseProcessExitTrigger([]byte(body))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if evt.ShortReason != "ER_CRASH" || evt.Reason != "segfault in worker" {
			t.Errorf("optional reasons not parsed: %+v", evt)
		}
		if evt.ExitCode != 137 {
			t.Errorf("ExitCode = %d, want 137", evt.ExitCode)
		}
	})

	t.Run("non-numeric numeric fields left at zero", func(t *testing.T) {
		body := strings.Join([]string{"s", "AV", "{}", "notapid", "x", "y", "retrying"}, "\n")
		evt, err := ParseProcessExitTrigger([]byte(body))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if evt.PID != 0 || evt.ExitCode != 0 || evt.BootCount != 0 {
			t.Errorf("unparseable numerics should stay zero: %+v", evt)
		}
		if evt.Status != "retrying" {
			t.Errorf("Status = %q, want retrying", evt.Status)
		}
	})
}
