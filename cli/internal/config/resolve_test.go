package config

import (
	"strings"
	"testing"
)

// TestResolveActiveContext_BehaviorMatrix verifies the 3-row matrix from
// the resolver spec: no-config, existing, and non-existent context.
func TestResolveActiveContext_BehaviorMatrix(t *testing.T) {
	existing := Context{Name: "prod"}
	cfg := Config{Current: "prod", Contexts: map[string]Context{"prod": existing}}
	empty := Config{}

	t.Run("no explicit, no env, empty current -> error", func(t *testing.T) {
		_, err := ResolveActiveContext(RuntimeOverrides{}, MapEnv{}, empty)
		if err == nil {
			t.Fatal("expected error on unconfigured context")
		}
		if !strings.Contains(err.Error(), "frameworks setup") {
			t.Errorf("error should point at setup: %v", err)
		}
	})

	t.Run("explicit flag names existing context -> returns it", func(t *testing.T) {
		ctx, err := ResolveActiveContext(RuntimeOverrides{ContextName: "prod", ContextExplicit: true}, MapEnv{}, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if ctx.Name != "prod" {
			t.Errorf("want prod, got %q", ctx.Name)
		}
	})

	t.Run("explicit flag names missing context -> error with options", func(t *testing.T) {
		_, err := ResolveActiveContext(RuntimeOverrides{ContextName: "nope", ContextExplicit: true}, MapEnv{}, cfg)
		if err == nil {
			t.Fatal("expected error on missing context")
		}
		if !strings.Contains(err.Error(), "prod") {
			t.Errorf("error should list available contexts: %v", err)
		}
	})

	t.Run("env names existing context", func(t *testing.T) {
		ctx, err := ResolveActiveContext(RuntimeOverrides{}, MapEnv{"FRAMEWORKS_CONTEXT": "prod"}, cfg)
		if err != nil || ctx.Name != "prod" {
			t.Errorf("want prod, got %q err %v", ctx.Name, err)
		}
	})

	t.Run("cfg.Current names existing context (no explicit, no env)", func(t *testing.T) {
		ctx, err := ResolveActiveContext(RuntimeOverrides{}, MapEnv{}, cfg)
		if err != nil || ctx.Name != "prod" {
			t.Errorf("want prod, got %q err %v", ctx.Name, err)
		}
	})

	t.Run("precedence: flag > env > current", func(t *testing.T) {
		twoCtx := Config{Current: "prod", Contexts: map[string]Context{
			"prod":    {Name: "prod"},
			"staging": {Name: "staging"},
			"dev":     {Name: "dev"},
		}}
		// flag "staging" > env "dev" > current "prod"
		ctx, err := ResolveActiveContext(
			RuntimeOverrides{ContextName: "staging", ContextExplicit: true},
			MapEnv{"FRAMEWORKS_CONTEXT": "dev"},
			twoCtx,
		)
		if err != nil || ctx.Name != "staging" {
			t.Errorf("want staging (flag wins), got %q err %v", ctx.Name, err)
		}
	})
}

// TestMaybeActiveContext_DistinguishesMissingFromUnconfigured verifies the
// tricky behavior distinction: returning zero Context when nothing is
// configured vs. erroring when something points at a non-existent name.
// This is the reason MaybeActive had to return (Context, error) and not
// just Context.
func TestMaybeActiveContext_DistinguishesMissingFromUnconfigured(t *testing.T) {
	cfg := Config{Contexts: map[string]Context{"prod": {Name: "prod"}}}

	t.Run("no explicit, no env, empty current -> zero context, nil", func(t *testing.T) {
		ctx, err := MaybeActiveContext(RuntimeOverrides{}, MapEnv{}, Config{})
		if err != nil {
			t.Errorf("expected nil error for unconfigured, got %v", err)
		}
		if ctx.Name != "" {
			t.Errorf("expected zero Context, got %q", ctx.Name)
		}
	})

	t.Run("explicit bad name still errors (doesn't silently fall through)", func(t *testing.T) {
		_, err := MaybeActiveContext(RuntimeOverrides{ContextName: "missing", ContextExplicit: true}, MapEnv{}, cfg)
		if err == nil {
			t.Fatal("MaybeActive must error on explicit non-existent name; zero-value fallback would be wrong")
		}
	})

	t.Run("flag not Changed is NOT an explicit opt-in (even if value non-empty)", func(t *testing.T) {
		// A defensive check: ContextName can be non-empty via some
		// plumbing bug, but if ContextExplicit is false we must still
		// fall through to env/current.
		ctx, err := MaybeActiveContext(RuntimeOverrides{ContextName: "garbage", ContextExplicit: false}, MapEnv{}, cfg)
		if err != nil {
			t.Errorf("non-Changed flag should not trigger lookup; got err %v", err)
		}
		if ctx.Name != "" {
			t.Errorf("expected zero Context, got %q", ctx.Name)
		}
	})

	t.Run("existing context returns it", func(t *testing.T) {
		ctx, err := MaybeActiveContext(RuntimeOverrides{ContextName: "prod", ContextExplicit: true}, MapEnv{}, cfg)
		if err != nil || ctx.Name != "prod" {
			t.Errorf("want prod, got %q err %v", ctx.Name, err)
		}
	})
}
