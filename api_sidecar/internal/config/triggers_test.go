package config

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// The Mist `default` for a blocking trigger is what Mist substitutes when the
// handler is unreachable or times out. A missing/`"true"` PLAY_REWRITE default
// routes playback to a stream named "true"; a `"true"` USER_NEW default admits
// viewers unauthenticated. Lock the safety-critical defaults so they can't
// silently regress.
func TestDesiredTriggersSafeDefaults(t *testing.T) {
	cfg := map[string]any{"triggers": desiredTriggers()}

	t.Run("PLAY_REWRITE defaults to the unresolved sentinel", func(t *testing.T) {
		got, ok := currentTriggerDefault(cfg, "PLAY_REWRITE")
		if !ok || got != PlayRewriteUnresolvedSentinel {
			t.Fatalf("PLAY_REWRITE default = %q (found=%v), want %q", got, ok, PlayRewriteUnresolvedSentinel)
		}
	})

	t.Run("USER_NEW fails closed", func(t *testing.T) {
		got, ok := currentTriggerDefault(cfg, "USER_NEW")
		if !ok || got != "false" {
			t.Fatalf("USER_NEW default = %q (found=%v), want \"false\"", got, ok)
		}
	})

	t.Run("no blocking trigger defaults to \"true\"", func(t *testing.T) {
		for name := range desiredTriggers() {
			if got, ok := currentTriggerDefault(cfg, name); ok && got == "true" {
				t.Fatalf("trigger %q defaults to \"true\" — Mist would admit/misroute on timeout", name)
			}
		}
	})
}

func TestCurrentTriggerDefaultUnknownShape(t *testing.T) {
	if _, ok := currentTriggerDefault(map[string]any{}, "PLAY_REWRITE"); ok {
		t.Fatal("unrecognized triggers block must report present=false")
	}
	// A present trigger with NO default must report present=true, def="" — the
	// caller treats a missing default as drift (Mist substitutes "true" for it).
	// present=false here would silently skip repair.
	noDefault := map[string]any{"triggers": map[string]any{
		"PLAY_REWRITE": []any{map[string]any{"handler": "x", "sync": true}},
	}}
	got, present := currentTriggerDefault(noDefault, "PLAY_REWRITE")
	if !present || got != "" {
		t.Fatalf("present trigger without default = (%q, %v), want (\"\", true)", got, present)
	}
}

// The drift repair must fire when a safety-critical default is MISSING, not
// only when it is present-but-wrong.
func TestRepairDetectsMissingDefaultAsDrift(t *testing.T) {
	// PLAY_REWRITE present but with no default.
	drifted := map[string]any{"triggers": map[string]any{
		"PLAY_REWRITE": []any{map[string]any{"handler": "x", "sync": true}},
		"USER_NEW":     []any{map[string]any{"handler": "x", "sync": true, "default": "false"}},
	}}
	if got, present := currentTriggerDefault(drifted, "PLAY_REWRITE"); !present || got == PlayRewriteUnresolvedSentinel {
		t.Fatalf("missing PLAY_REWRITE default must be observable as drift: got=%q present=%v", got, present)
	}
}

// End-to-end: a running Mist whose PLAY_REWRITE has no default must be
// repaired — repairTriggerDefaults re-applies the full trigger set.
func TestRepairTriggerDefaultsReappliesOnMissingDefault(t *testing.T) {
	mist := &recordingMistAPI{backupResult: map[string]interface{}{
		"triggers": map[string]any{
			"PLAY_REWRITE": []any{map[string]any{"handler": "x", "sync": true}}, // no default
			"USER_NEW":     []any{map[string]any{"handler": "x", "sync": true, "default": "false"}},
		},
	}}
	m := &Manager{mistClient: mist, logger: logging.NewLogger()}
	m.repairTriggerDefaults()
	if len(mist.updatedConfigs) != 1 {
		t.Fatalf("expected one UpdateConfig re-apply, got %d", len(mist.updatedConfigs))
	}
	if _, ok := mist.updatedConfigs[0]["triggers"]; !ok {
		t.Fatalf("re-apply must push the triggers block, got %v", mist.updatedConfigs[0])
	}

	// Healthy config (both safe) → no re-apply.
	mist.updatedConfigs = nil
	mist.backupResult = map[string]interface{}{"triggers": desiredTriggers()}
	m.repairTriggerDefaults()
	if len(mist.updatedConfigs) != 0 {
		t.Fatalf("safe config must not be re-applied, got %d updates", len(mist.updatedConfigs))
	}
}
