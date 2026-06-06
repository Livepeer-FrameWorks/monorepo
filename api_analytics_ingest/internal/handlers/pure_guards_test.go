package handlers

import (
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/google/uuid"
)

func strptr(s string) *string { return &s }

func TestParseTenantID(t *testing.T) {
	// Intent: tenant_id is a trust boundary for analytics writes. parseTenantID
	// must accept only a real (non-Nil) UUID after trimming, and reject empty,
	// whitespace-only, malformed, and the all-zero Nil UUID — each returning
	// ok=false so callers drop the row rather than attribute it to tenant Nil.
	valid := "11111111-1111-1111-1111-111111111111"

	t.Run("valid trimmed uuid", func(t *testing.T) {
		got, ok := parseTenantID("  " + valid + "  ")
		if !ok {
			t.Fatal("expected ok=true for a valid uuid")
		}
		if got.String() != valid {
			t.Fatalf("parsed = %q, want %q", got.String(), valid)
		}
	})

	rejects := []struct {
		name, in string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"malformed", "not-a-uuid"},
		{"nil uuid", uuid.Nil.String()},
	}
	for _, tt := range rejects {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseTenantID(tt.in)
			if ok {
				t.Fatalf("parseTenantID(%q) = ok, want reject", tt.in)
			}
			if got != uuid.Nil {
				t.Fatalf("rejected parse must return uuid.Nil, got %v", got)
			}
		})
	}
}

func TestMistTriggerStreamID(t *testing.T) {
	// Intent: pin the stream-id resolution precedence. A valid top-level UUID
	// wins outright; otherwise the typed payload's stream_id is used but only if
	// it is itself a valid UUID; a nil trigger yields ""; and when nothing
	// resolves the function falls back to the (possibly empty) top-level value.
	valid := "22222222-2222-2222-2222-222222222222"
	payloadUUID := "33333333-3333-3333-3333-333333333333"

	t.Run("nil trigger", func(t *testing.T) {
		if got := mistTriggerStreamID(nil); got != "" {
			t.Fatalf("nil trigger = %q, want empty", got)
		}
	})

	t.Run("valid top-level uuid wins", func(t *testing.T) {
		mt := &ipcpb.MistTrigger{StreamId: strptr(valid)}
		if got := mistTriggerStreamID(mt); got != valid {
			t.Fatalf("got %q, want %q", got, valid)
		}
	})

	t.Run("invalid top-level falls to valid payload uuid", func(t *testing.T) {
		mt := &ipcpb.MistTrigger{
			StreamId: strptr("garbage"),
			TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
				PushRewrite: &ipcpb.PushRewriteTrigger{StreamId: strptr(payloadUUID)},
			},
		}
		if got := mistTriggerStreamID(mt); got != payloadUUID {
			t.Fatalf("got %q, want payload uuid %q", got, payloadUUID)
		}
	})

	t.Run("non-uuid payload yields empty", func(t *testing.T) {
		mt := &ipcpb.MistTrigger{
			StreamId: strptr("garbage"),
			TriggerPayload: &ipcpb.MistTrigger_PushRewrite{
				PushRewrite: &ipcpb.PushRewriteTrigger{StreamId: strptr("also-garbage")},
			},
		}
		if got := mistTriggerStreamID(mt); got != "" {
			t.Fatalf("got %q, want empty for non-uuid payload", got)
		}
	})

	t.Run("no payload falls back to raw top-level", func(t *testing.T) {
		// Top-level is non-empty but not a UUID and there is no typed payload:
		// the fallback returns the raw top-level value as-is.
		mt := &ipcpb.MistTrigger{StreamId: strptr("legacy-name")}
		if got := mistTriggerStreamID(mt); got != "legacy-name" {
			t.Fatalf("got %q, want fallback legacy-name", got)
		}
	})
}
