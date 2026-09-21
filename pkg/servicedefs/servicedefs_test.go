package servicedefs

import "testing"

func TestGRPCServicesIncludeRuntimeDependencyEndpoints(t *testing.T) {
	want := map[string]GRPCService{
		"commodore":       {ServiceID: "commodore", EnvKey: "COMMODORE_GRPC_ADDR", Port: 19001},
		"quartermaster":   {ServiceID: "quartermaster", EnvKey: "QUARTERMASTER_GRPC_ADDR", Port: 19002},
		"purser":          {ServiceID: "purser", EnvKey: "PURSER_GRPC_ADDR", Port: 19003},
		"periscope-query": {ServiceID: "periscope-query", EnvKey: "PERISCOPE_GRPC_ADDR", Port: 19004},
		"signalman":       {ServiceID: "signalman", EnvKey: "SIGNALMAN_GRPC_ADDR", Port: 19005},
		"decklog":         {ServiceID: "decklog", EnvKey: "DECKLOG_GRPC_ADDR", Port: 18006},
		"deckhand":        {ServiceID: "deckhand", EnvKey: "DECKHAND_GRPC_ADDR", Port: 19006},
		"skipper":         {ServiceID: "skipper", EnvKey: "SKIPPER_GRPC_ADDR", Port: 19007},
		"navigator":       {ServiceID: "navigator", EnvKey: "NAVIGATOR_GRPC_ADDR", Port: 18011},
		"foghorn":         {ServiceID: "foghorn", EnvKey: "FOGHORN_GRPC_ADDR", Port: 18019},
		"lookout":         {ServiceID: "lookout", EnvKey: "LOOKOUT_GRPC_ADDR", Port: 19008},
	}

	got := make(map[string]GRPCService)
	for _, svc := range GRPCServices() {
		got[svc.ServiceID] = svc
	}
	for serviceID, expected := range want {
		if got[serviceID] != expected {
			t.Fatalf("GRPCServices()[%s] = %+v, want %+v", serviceID, got[serviceID], expected)
		}
	}
}

// TestLookupAndDefaultPort pins the registry-lookup contract that manifest
// rendering and service discovery depend on: a known ID returns its definition
// and port, and an unknown ID returns the zero value with ok=false (never a
// silent zero-port that would point a consumer at port 0).
func TestLookupAndDefaultPort(t *testing.T) {
	t.Run("known service resolves port", func(t *testing.T) {
		svc, ok := Lookup("foghorn")
		if !ok {
			t.Fatal("Lookup(foghorn) ok=false, want true")
		}
		if svc.DefaultPort != 18008 {
			t.Fatalf("foghorn DefaultPort = %d, want 18008", svc.DefaultPort)
		}
		port, ok := DefaultPort("foghorn")
		if !ok || port != 18008 {
			t.Fatalf("DefaultPort(foghorn) = (%d, %v), want (18008, true)", port, ok)
		}
	})

	t.Run("unknown service is not ok", func(t *testing.T) {
		if _, ok := Lookup("does-not-exist"); ok {
			t.Fatal("Lookup(unknown) ok=true, want false")
		}
		port, ok := DefaultPort("does-not-exist")
		if ok || port != 0 {
			t.Fatalf("DefaultPort(unknown) = (%d, %v), want (0, false)", port, ok)
		}
	})
}

// TestSupportsSIGHUPReload pins the safe-default behavior of the reload gate:
// services that have wired a reload callback report true, services that have
// not (and unknown IDs) report false. A false default is what keeps an
// unrecognized manifest entry from rendering ExecReload= and reload-firing a
// process that never registered a callback.
func TestSupportsSIGHUPReload(t *testing.T) {
	cases := []struct {
		id   string
		want bool
	}{
		{"bridge", true},      // control-plane service with a registered callback
		{"foghorn", true},     // media-plane service with a registered callback
		{"mistserver", false}, // third-party binary, no callback
		{"privateer", false},  // opted out
		{"deckhand", false},   // startup-only configuration
		{"steward", true},
		{"skipper", true},
		{"does-not-exist", false}, // unknown → never accidentally reload-enabled
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			if got := SupportsSIGHUPReload(tc.id); got != tc.want {
				t.Fatalf("SupportsSIGHUPReload(%s) = %v, want %v", tc.id, got, tc.want)
			}
		})
	}
}

func TestNavigatorRequiresExplicitACMEEmail(t *testing.T) {
	for _, req := range RequiredExternalEnv("navigator") {
		if req.Key == "ACME_EMAIL" {
			return
		}
	}
	t.Fatal("navigator must require ACME_EMAIL so certificate issuance never falls back to a platform contact")
}

// Lookout exits at startup without the webhook token, and the Alertmanager
// role refuses to render without its routing and SMTP inputs, so both are
// declared operator inputs that provisioning and release preflights check
// before any host changes.
func TestAlertingServicesRequireOperatorInputs(t *testing.T) {
	want := map[string][]string{
		"lookout":      {"LOOKOUT_ALERTMANAGER_TOKEN"},
		"alertmanager": {"LOOKOUT_ALERTMANAGER_TOKEN", "ALERTMANAGER_HEARTBEAT_URL", "ALERTMANAGER_EMAIL_TO", "SMTP_HOST", "SMTP_PORT", "FROM_EMAIL"},
	}
	for serviceID, keys := range want {
		declared := map[string]bool{}
		for _, req := range RequiredExternalEnv(serviceID) {
			declared[req.Key] = true
		}
		for _, key := range keys {
			if !declared[key] {
				t.Errorf("%s must require %s", serviceID, key)
			}
		}
	}
}

func TestListmonkRequiresAdminCredsFromGitOps(t *testing.T) {
	required := RequiredExternalEnv("listmonk")
	want := map[string]bool{
		"LISTMONK_ADMIN_USER":     false,
		"LISTMONK_ADMIN_PASSWORD": false,
	}
	for _, req := range required {
		if _, ok := want[req.Key]; ok {
			want[req.Key] = true
		}
	}
	for key, found := range want {
		if !found {
			t.Fatalf("listmonk must require %s so first-install admin creds are GitOps-controlled, not Listmonk's installer defaults", key)
		}
	}
}

func TestDeliveryClassFor(t *testing.T) {
	for _, dependency := range []string{"nginx", "caddy", "victoriametrics", "vmagent", "vmalert", "alertmanager", "grafana", "mistserver", "livepeer-gateway"} {
		if got := DeliveryClassFor(dependency); got != DeliveryManagedDependency {
			t.Errorf("DeliveryClassFor(%s) = %s, want %s", dependency, got, DeliveryManagedDependency)
		}
	}
	for _, infra := range []string{"postgres", "kafka", "clickhouse", "redis"} {
		if got := DeliveryClassFor(infra); got != DeliveryHostInfrastructure {
			t.Errorf("DeliveryClassFor(%s) = %s, want %s", infra, got, DeliveryHostInfrastructure)
		}
	}
	for _, platform := range []string{"foghorn", "chandler", "commodore", "quartermaster", "chartroom", "foredeck", "logbook"} {
		if got := DeliveryClassFor(platform); got != DeliveryPlatformArtifact {
			t.Errorf("DeliveryClassFor(%s) = %s, want %s", platform, got, DeliveryPlatformArtifact)
		}
	}
}

// Bosun's field-encryption key is generated by the operator, not obtained
// from a running service, so it is not an external input that may defer the
// start: the schema contract refuses a deploy without it.
func TestBosunFieldEncryptionKeyIsNotExternal(t *testing.T) {
	svc, ok := Lookup("bosun")
	if !ok || svc.DefaultPort != 18013 {
		t.Fatalf("Lookup(bosun) = %+v, %v; want HTTP port 18013", svc, ok)
	}
	if port, ok := DefaultGRPCPort("bosun"); !ok || port != 19009 {
		t.Fatalf("DefaultGRPCPort(bosun) = %d, %v; want 19009", port, ok)
	}
	for _, req := range RequiredExternalEnv("bosun") {
		if req.Key == "BOSUN_FIELD_ENCRYPTION_KEY" {
			t.Fatal("BOSUN_FIELD_ENCRYPTION_KEY must not be external; a missing key must fail the contract, not defer the start")
		}
	}
}
