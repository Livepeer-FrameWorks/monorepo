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

func TestNavigatorRequiresExplicitACMEEmail(t *testing.T) {
	for _, req := range RequiredExternalEnv("navigator") {
		if req.Key == "ACME_EMAIL" {
			return
		}
	}
	t.Fatal("navigator must require ACME_EMAIL so certificate issuance never falls back to a platform contact")
}

func TestListmonkRequiresAdminCredsFromGitOps(t *testing.T) {
	required := RequiredExternalEnv("listmonk")
	want := map[string]bool{
		"LISTMONK_USERNAME": false,
		"LISTMONK_PASSWORD": false,
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
