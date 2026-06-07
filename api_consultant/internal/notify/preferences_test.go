package notify

import "testing"

func TestResolvePreferences(t *testing.T) {
	defaults := PreferenceDefaults{Email: true, Websocket: true, MCP: false}

	t.Run("nil_overrides_returns_defaults", func(t *testing.T) {
		got := ResolvePreferences(defaults, nil)
		if got != defaults {
			t.Fatalf("got %+v, want defaults %+v", got, defaults)
		}
	})

	t.Run("each_override_replaces_its_field", func(t *testing.T) {
		got := ResolvePreferences(defaults, &NotificationPreferences{
			Email:     new(false),
			Websocket: new(false),
			MCP:       new(true),
		})
		want := PreferenceDefaults{Email: false, Websocket: false, MCP: true}
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})

	t.Run("partial_override_leaves_others_at_default", func(t *testing.T) {
		got := ResolvePreferences(defaults, &NotificationPreferences{
			MCP: new(true),
		})
		want := PreferenceDefaults{Email: true, Websocket: true, MCP: true}
		if got != want {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	})
}
