package servicedefs

import "testing"

// goHTTPServices are the Go services built on pkg/server.NewServiceRouter, which serves /ready from the release that
// declares ReadySince. Chandler is listed separately because its store-backed /ready predates the shared router.
var goHTTPServices = []string{
	"bridge", "commodore", "quartermaster", "purser",
	"periscope-query", "periscope-ingest", "periscope-metering",
	"decklog", "signalman", "foghorn", "helmsman",
	"navigator", "privateer", "lookout", "bosun",
	"skipper", "deckhand", "steward",
}

func TestGoServicesDeclareReadyPathWithIntroducingRelease(t *testing.T) {
	for _, id := range goHTTPServices {
		svc, ok := Lookup(id)
		if !ok {
			t.Fatalf("servicedefs missing %q", id)
		}
		if svc.ReadyPath != "/ready" {
			t.Errorf("%s ReadyPath = %q, want /ready", id, svc.ReadyPath)
		}
		if svc.ReadySince == "" {
			t.Errorf("%s ReadySince is empty; binaries of shipped releases do not serve /ready", id)
		}
	}
	chandler := Services["chandler"]
	if chandler.ReadyPath != "/ready" || chandler.ReadySince != "" {
		t.Fatalf("chandler = {ReadyPath:%q ReadySince:%q}, want /ready served by every supported release", chandler.ReadyPath, chandler.ReadySince)
	}
}

// TestReadinessPathIsLivenessWhileOlderBinariesMayRun pins the fleet-wide path used by consumers that cannot see the
// probed binary's version (Quartermaster's poller, Navigator's monitors, the rendered registry, self-registration, the
// doctor, and the orchestrator gate). While ReadySince is set, a binary from before that release may still be running,
// so these consumers stay on HealthPath.
func TestReadinessPathIsLivenessWhileOlderBinariesMayRun(t *testing.T) {
	for _, id := range goHTTPServices {
		svc := Services[id]
		if got := svc.ReadinessPath(); got != svc.HealthPath {
			t.Errorf("%s ReadinessPath() = %q, want HealthPath %q while ReadySince=%s", id, got, svc.HealthPath, svc.ReadySince)
		}
	}
	if got := Services["chandler"].ReadinessPath(); got != "/ready" {
		t.Fatalf("chandler ReadinessPath() = %q, want /ready", got)
	}
	if got := Services["mistserver"].ReadinessPath(); got != "/metrics" {
		t.Fatalf("mistserver ReadinessPath() = %q, want its HealthPath /metrics", got)
	}
}

// TestReadinessPathForSelectsByBinaryVersion covers the consumers that know which binary they are probing: the
// native and Compose rollout gates for a forward deploy, an automatic rollback, and each host of a rolling upgrade.
func TestReadinessPathForSelectsByBinaryVersion(t *testing.T) {
	svc := Service{ID: "example", HealthPath: "/health", ReadyPath: "/ready", ReadySince: "v0.3.11"}
	cases := []struct {
		name    string
		version string
		want    string
	}{
		{"upgrade target serves /ready", "v0.3.11", "/ready"},
		{"later release", "v0.4.0", "/ready"},
		{"release candidate of the introducing release", "v0.3.11-rc1", "/ready"},
		{"build past the introducing tag", "v0.3.11-4-gabcdef1", "/ready"},
		{"rollback to a release without /ready", "v0.3.10", "/health"},
		{"older minor", "v0.2.96", "/health"},
		{"build past an older tag", "v0.3.10-7-gabcdef1", "/health"},
		{"missing v prefix", "0.3.11", "/ready"},
		{"surrounding whitespace", " v0.3.11 ", "/ready"},
		{"unknown version", "", "/health"},
		{"dev build", "dev", "/health"},
		{"channel selector", "stable", "/health"},
		{"digest", "sha256:0123456789abcdef", "/health"},
		{"overflowing component", "v0.3.99999999999999999999", "/health"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := svc.ReadinessPathFor(tc.version); got != tc.want {
				t.Fatalf("ReadinessPathFor(%q) = %q, want %q", tc.version, got, tc.want)
			}
		})
	}

	always := Service{ID: "chandler", HealthPath: "/health", ReadyPath: "/ready"}
	for _, v := range []string{"", "dev", "v0.3.0"} {
		if got := always.ReadinessPathFor(v); got != "/ready" {
			t.Fatalf("chandler-style ReadinessPathFor(%q) = %q, want /ready", v, got)
		}
	}
	liveOnly := Service{ID: "grafana", HealthPath: "/api/health"}
	if got := liveOnly.ReadinessPathFor("v9.0.0"); got != "/api/health" {
		t.Fatalf("service without ReadyPath = %q, want HealthPath", got)
	}
}

// TestReadinessPolledFollowsVersionBlindPollers: pkg/server delays a signalled stop only for services whose pollers
// probe /ready. Chandler is polled on /ready in every supported release; a service with ReadySince set is still polled
// on /health; once ReadySince is dropped it is polled on /ready; Helmsman never is.
func TestReadinessPolledFollowsVersionBlindPollers(t *testing.T) {
	if !Services["chandler"].ReadinessPolled() {
		t.Fatal("chandler is polled on /ready")
	}
	bridge := Services["bridge"]
	if bridge.ReadySince != "" && bridge.ReadinessPolled() {
		t.Fatal("bridge with ReadySince set is polled on /health")
	}
	bridge.ReadySince = ""
	if !bridge.ReadinessPolled() {
		t.Fatal("bridge without ReadySince is polled on /ready")
	}
	helmsman := Services["helmsman"]
	helmsman.ReadySince = ""
	if helmsman.ReadinessPolled() {
		t.Fatal("helmsman readiness is never polled")
	}
	if (Service{ID: "grafana", HealthPath: "/api/health"}).ReadinessPolled() {
		t.Fatal("a service without ReadyPath is not readiness-polled")
	}
}

// TestReadinessPathForMixedFleet walks a rolling upgrade from a release without /ready: each host is probed for the
// binary it runs, so the upgraded hosts gate on /ready while the hosts still on the source release gate on /health.
func TestReadinessPathForMixedFleet(t *testing.T) {
	svc := Services["bridge"]
	fleet := map[string]string{"host-a": svc.ReadySince, "host-b": "v0.3.10", "host-c": svc.ReadySince + "-rc2"}
	want := map[string]string{"host-a": "/ready", "host-b": "/health", "host-c": "/ready"}
	for host, version := range fleet {
		if got := svc.ReadinessPathFor(version); got != want[host] {
			t.Errorf("%s on %s: path = %q, want %q", host, version, got, want[host])
		}
	}
}
