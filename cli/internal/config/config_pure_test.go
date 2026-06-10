package config

import "testing"

func TestDefaultEndpointsAreLocalhost(t *testing.T) {
	ep := defaultEndpoints()
	// Every default endpoint must point at localhost — the default set is the
	// local-dev starting point for the setup wizard.
	for name, v := range map[string]string{
		"bridge":        ep.BridgeURL,
		"quartermaster": ep.QuartermasterGRPCAddr,
		"commodore":     ep.CommodoreGRPCAddr,
		"signalman ws":  ep.SignalmanWSURL,
	} {
		if !IsLocalhostEndpoint(v) {
			t.Errorf("%s endpoint %q is not localhost", name, v)
		}
	}
	if DefaultEndpoints() != ep {
		t.Errorf("DefaultEndpoints() should equal defaultEndpoints()")
	}
}

func TestIsLocalhostEndpoint(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"localhost:19001", true},
		{"127.0.0.1:8080", true},
		{"http://localhost:18000", true},
		{"ws://127.0.0.1:18009", true},
		{"https://api.example.com", false},
		{"grpc.prod.internal:443", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := IsLocalhostEndpoint(tt.in); got != tt.want {
				t.Fatalf("IsLocalhostEndpoint(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsLocalContext(t *testing.T) {
	local := Context{Endpoints: Endpoints{BridgeURL: "http://localhost:18000"}}
	remote := Context{Endpoints: Endpoints{BridgeURL: "https://bridge.example.com"}}
	if !IsLocalContext(local) {
		t.Errorf("localhost bridge should be local context")
	}
	if IsLocalContext(remote) {
		t.Errorf("remote bridge should not be local context")
	}
}

// RequireEndpoint encodes the operator-facing diagnostics: a different message
// for local vs named contexts when unset, and a misconfiguration guard when a
// localhost endpoint is paired with a non-local context.
func TestRequireEndpoint(t *testing.T) {
	localCtx := Context{Name: "dev", Endpoints: Endpoints{BridgeURL: "http://localhost:18000"}}
	remoteCtx := Context{Name: "prod", Endpoints: Endpoints{BridgeURL: "https://bridge.example.com"}}

	// Valid endpoint passes through unchanged.
	if got, err := RequireEndpoint(remoteCtx, "commodore", "grpc.prod:443", false); err != nil || got != "grpc.prod:443" {
		t.Fatalf("valid endpoint: got %q err %v", got, err)
	}

	// Unset on a local context -> setup hint.
	if _, err := RequireEndpoint(localCtx, "commodore", "", false); err == nil {
		t.Fatalf("expected error for unset endpoint on local context")
	}

	// Unset on a remote context -> set-url hint (different message, both error).
	if _, err := RequireEndpoint(remoteCtx, "commodore", "", false); err == nil {
		t.Fatalf("expected error for unset endpoint on remote context")
	}

	// Localhost endpoint on a non-local context, localhost disallowed -> error.
	if _, err := RequireEndpoint(remoteCtx, "commodore", "localhost:19001", false); err == nil {
		t.Fatalf("expected misconfiguration error for localhost endpoint on remote context")
	}

	// Same, but localhost explicitly allowed -> passes.
	if got, err := RequireEndpoint(remoteCtx, "commodore", "localhost:19001", true); err != nil || got != "localhost:19001" {
		t.Fatalf("allowLocalhost should permit: got %q err %v", got, err)
	}
}

func TestPersonaIsUser(t *testing.T) {
	if !PersonaUser.IsUser() {
		t.Errorf("PersonaUser should be a user")
	}
	if !PersonaEdge.IsUser() {
		t.Errorf("PersonaEdge (deprecated alias) should be a user")
	}
	for _, p := range []Persona{PersonaPlatform, PersonaSelfHosted, Persona("")} {
		if p.IsUser() {
			t.Errorf("%q should not be a user", p)
		}
	}
}

func TestEffectiveAccessMode(t *testing.T) {
	// Empty access mode defaults to local (back-compat for older contexts).
	if got := (Context{}).EffectiveAccessMode(); got != AccessModeLocal {
		t.Errorf("empty access mode = %q, want local", got)
	}
	if got := (Context{AccessMode: AccessModeSSH}).EffectiveAccessMode(); got != AccessModeSSH {
		t.Errorf("explicit access mode = %q, want ssh", got)
	}
}

func TestRuntimeOverridesRoundTrip(t *testing.T) {
	saved := GetRuntimeOverrides()
	t.Cleanup(func() { SetRuntimeOverrides(saved) }) // global state — restore for sibling tests

	want := RuntimeOverrides{ContextName: "prod", ContextExplicit: true, OutputJSON: true}
	SetRuntimeOverrides(want)
	if got := GetRuntimeOverrides(); got != want {
		t.Fatalf("GetRuntimeOverrides() = %+v, want %+v", got, want)
	}
}
