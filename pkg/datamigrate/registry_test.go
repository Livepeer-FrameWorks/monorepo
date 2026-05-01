package datamigrate

import (
	"context"
	"errors"
	"testing"
)

func TestRegistry_EmptyByDefault(t *testing.T) {
	resetForTest()
	if got := Registry(); len(got) != 0 {
		t.Errorf("empty registry expected, got %d", len(got))
	}
	if Lookup("anything") != nil {
		t.Error("Lookup must return nil for unknown id")
	}
	if got := ByService("purser"); len(got) != 0 {
		t.Errorf("ByService empty registry must return empty; got %d", len(got))
	}
}

func TestRegistry_RegisterAndLookup(t *testing.T) {
	resetForTest()
	noop := func(_ context.Context, _ DB, _ RunOptions) (Progress, error) {
		return Progress{Done: true}, nil
	}
	Register(Migration{ID: "a", Service: "purser", IntroducedIn: "v0.5.0", Run: noop})
	Register(Migration{ID: "b", Service: "purser", IntroducedIn: "v0.4.0", Run: noop})
	Register(Migration{ID: "c", Service: "qm", IntroducedIn: "v0.5.0", Run: noop})

	all := Registry()
	if len(all) != 3 {
		t.Fatalf("got %d registered, want 3", len(all))
	}
	// Sort: (Service, IntroducedIn, ID) — purser/v0.4.0/b, purser/v0.5.0/a, qm/v0.5.0/c
	if all[0].ID != "b" || all[1].ID != "a" || all[2].ID != "c" {
		t.Errorf("wrong sort: %+v", []string{all[0].ID, all[1].ID, all[2].ID})
	}

	if Lookup("a") == nil {
		t.Error("Lookup(a) returned nil")
	}
	if Lookup("missing") != nil {
		t.Error("Lookup(missing) must return nil")
	}

	purser := ByService("purser")
	if len(purser) != 2 {
		t.Errorf("ByService(purser) got %d, want 2", len(purser))
	}
}

func TestRegistry_DuplicatePanics(t *testing.T) {
	resetForTest()
	noop := func(_ context.Context, _ DB, _ RunOptions) (Progress, error) {
		return Progress{Done: true}, nil
	}
	Register(Migration{ID: "x", Service: "s", Run: noop})
	defer func() {
		if recover() == nil {
			t.Error("duplicate Register must panic")
		}
	}()
	Register(Migration{ID: "x", Service: "s", Run: noop})
}

func TestRegistry_RejectsEmptyFields(t *testing.T) {
	resetForTest()
	noop := func(_ context.Context, _ DB, _ RunOptions) (Progress, error) {
		return Progress{Done: true}, nil
	}
	cases := []Migration{
		{Service: "s", Run: noop}, // empty ID
		{ID: "x", Run: noop},      // empty Service
		{ID: "x", Service: "s"},   // nil Run
	}
	for i, c := range cases {
		func(c Migration, i int) {
			defer func() {
				if recover() == nil {
					t.Errorf("case %d: must panic on missing required field", i)
				}
			}()
			Register(c)
		}(c, i)
	}
}

func TestIsNotRegistered(t *testing.T) {
	if !IsNotRegistered(&NotRegisteredError{ID: "x"}) {
		t.Error("IsNotRegistered must accept *NotRegisteredError")
	}
	if IsNotRegistered(nil) {
		t.Error("IsNotRegistered(nil) must be false")
	}
	if IsNotRegistered(errors.New("other")) {
		t.Error("IsNotRegistered must reject non-typed errors")
	}
}

func TestScopeKey_String(t *testing.T) {
	zero := ScopeKey{}
	if zero.String() != "<whole-job>" {
		t.Errorf("zero scope String wrong: %q", zero.String())
	}
	tenant := ScopeKey{Kind: "tenant", Value: "abc"}
	if got := tenant.String(); got != "tenant=abc" {
		t.Errorf("ScopeKey String got %q", got)
	}
}
