package config

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestDesiredTriggersTypedFailurePolicy(t *testing.T) {
	got, ok := normalizeTriggerConfig(desiredTriggers())
	if !ok {
		t.Fatal("desired trigger definitions must normalize")
	}
	if len(got) != 18 {
		t.Fatalf("managed trigger count = %d, want 18", len(got))
	}
	want := map[string]string{
		"PUSH_REWRITE":   "deny",
		"PLAY_REWRITE":   "deny",
		"USER_NEW":       "deny",
		"STREAM_SOURCE":  "offline",
		"STREAM_PROCESS": "keep",
		"PUSH_OUT_START": "keep",
	}
	for name, onFail := range want {
		entries := got[name]
		if len(entries) != 1 || entries[0].OnFail != onFail || !entries[0].Sync {
			t.Fatalf("%s = %+v, want one blocking definition with onfail=%q", name, entries, onFail)
		}
	}
	for name, entries := range got {
		if len(entries) != 1 {
			t.Fatalf("%s has %d entries, want one", name, len(entries))
		}
		if !entries[0].Sync && entries[0].OnFail != "" {
			t.Fatalf("async trigger %s has onfail=%q", name, entries[0].OnFail)
		}
	}
}

func TestValidateTriggerDefinitionsRejectsMistStringTruncation(t *testing.T) {
	triggers := desiredTriggers()
	triggers["PLAY_REWRITE"] = []any{TriggerDefinition{
		Handler: strings.Repeat("x", mistTriggerStringLimit+1),
		Sync:    true,
		OnFail:  "deny",
	}}
	if err := validateTriggerDefinitions(triggers); err == nil {
		t.Fatal("expected an overlong handler URL to be rejected")
	}
}

func TestNormalizeTriggerConfigRoundTrip(t *testing.T) {
	want, ok := normalizeTriggerConfig(desiredTriggers())
	if !ok {
		t.Fatal("desired definitions must normalize")
	}
	encodedShape := map[string]any{}
	for name, entries := range want {
		entry := entries[0]
		encodedShape[name] = []any{map[string]any{
			"handler": entry.Handler,
			"sync":    entry.Sync,
			"streams": entry.Streams,
			"params":  entry.Params,
			"default": entry.Default,
			"onfail":  entry.OnFail,
		}}
	}
	got, ok := normalizeTriggerConfig(encodedShape)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("round-tripped definitions differ\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestRepairTriggerDefinitions(t *testing.T) {
	t.Run("missing entry", func(t *testing.T) {
		current := desiredTriggers()
		delete(current, "USER_NEW")
		mist := &recordingMistAPI{backupResult: map[string]interface{}{"triggers": current}}
		m := &Manager{mistClient: mist, logger: logging.NewLogger()}
		m.repairTriggerDefinitions()
		if len(mist.updatedConfigs) != 1 {
			t.Fatalf("updates = %d, want one", len(mist.updatedConfigs))
		}
	})

	t.Run("field drift", func(t *testing.T) {
		current := desiredTriggers()
		current["PLAY_REWRITE"] = []any{TriggerDefinition{Handler: "http://wrong", Sync: true, OnFail: "keep"}}
		mist := &recordingMistAPI{backupResult: map[string]interface{}{"triggers": current}}
		m := &Manager{mistClient: mist, logger: logging.NewLogger()}
		m.repairTriggerDefinitions()
		if len(mist.updatedConfigs) != 1 {
			t.Fatalf("updates = %d, want one", len(mist.updatedConfigs))
		}
	})

	t.Run("stable round trip", func(t *testing.T) {
		mist := &recordingMistAPI{backupResult: map[string]interface{}{"triggers": desiredTriggers()}}
		m := &Manager{mistClient: mist, logger: logging.NewLogger()}
		m.repairTriggerDefinitions()
		if len(mist.updatedConfigs) != 0 {
			t.Fatalf("stable config caused %d updates", len(mist.updatedConfigs))
		}
	})
}
