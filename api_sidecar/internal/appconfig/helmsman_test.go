package appconfig

import (
	"errors"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

func requiredValues() map[string]string {
	return map[string]string{
		"NODE_ID":              "edge-1",
		"FOGHORN_CONTROL_ADDR": "foghorn:18019",
		"MISTSERVER_URL":       "http://localhost:4242",
		"EDGE_PUBLIC_URL":      "https://edge.example/view",
		"HELMSMAN_STATE_DIR":   "/data/state",
	}
}

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func loadWith(overrides map[string]string) (*Helmsman, error) {
	values := requiredValues()
	for key, value := range overrides {
		values[key] = value
	}
	return config.Load[Helmsman](config.Options{Service: ServiceID, Lookup: lookupFrom(values)})
}

func mustLoad(t *testing.T, overrides map[string]string) *Helmsman {
	t.Helper()
	cfg, err := loadWith(overrides)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

func installForTest(t *testing.T, live *config.Live[Helmsman]) {
	t.Helper()
	previous := Install(live)
	t.Cleanup(func() { Install(previous) })
}

func TestHelmsmanDefaultsMatchLegacyReaders(t *testing.T) {
	cfg := mustLoad(t, nil)

	strs := map[string][2]string{
		"PORT":                          {cfg.Port, "18007"},
		"HELMSMAN_MANAGEMENT_PORT":      {cfg.ManagementPort, strconv.Itoa(servicedefs.HelmsmanManagementPort)},
		"HELMSMAN_MANAGEMENT_BIND_ADDR": {cfg.ManagementBindAddr, "127.0.0.1"},
		"HELMSMAN_BIND_ADDR":            {cfg.PublicBindAddr, ""},
		"GIN_MODE":                      {cfg.GinMode, "debug"},
		"HELMSMAN_OPERATIONAL_MODE":     {cfg.RequestedOperationalMode, "normal"},
		"HELMSMAN_WEBHOOK_URL":          {cfg.MistWebhookBaseURL, "http://localhost:18007"},
		"HELMSMAN_RELAY_BASE_URL":       {cfg.RelayBaseURL, "http://127.0.0.1:18007"},
		"HELMSMAN_TLS_CERT_PATH":        {cfg.EdgeTLSCertPath, "/etc/frameworks/certs/cert.pem"},
		"HELMSMAN_TLS_KEY_PATH":         {cfg.EdgeTLSKeyPath, "/etc/frameworks/certs/key.pem"},
		"HELMSMAN_TLS_BUNDLE_DIR":       {cfg.EdgeTLSBundleDir, "/etc/frameworks/certs/bundles"},
		"CADDY_CONFIG_PATH":             {cfg.CaddyConfigPath, "/etc/caddy/Caddyfile"},
		"CHANDLER_URL":                  {cfg.ChandlerUpstream, "chandler:18020"},
		"MISTSERVER_HTTP_URL":           {cfg.MistHTTPUpstream, "http://mistserver:8080"},
		"DEPLOY_MODE":                   {cfg.DeployMode, "native"},
		"MIST_ONNX_PROFILE":             {cfg.MistONNXProfile, "cpu"},
		"GRPC_TLS_CA_PATH":              {cfg.GRPCTLSCAPath, ""},
		"CADDY_ADMIN_URL":               {cfg.CaddyAdminURL, ""},
		"storage path fallback":         {cfg.StoragePathOrDefault(), "/var/lib/frameworks/edge-storage"},
	}
	for key, pair := range strs {
		if pair[0] != pair[1] {
			t.Errorf("%s = %q, want %q", key, pair[0], pair[1])
		}
	}
	if got := cfg.BlockingGrace(); got != 2000 {
		t.Errorf("BlockingGrace() = %d, want 2000", got)
	}
	if freeze, target := cfg.StorageThresholds(); freeze != 0.85 || target != 0.70 {
		t.Errorf("StorageThresholds() = %v/%v, want 0.85/0.70", freeze, target)
	}
	if ingest, edge, storage, processing := cfg.Capabilities(); !ingest || !edge || !storage || !processing {
		t.Errorf("Capabilities() = %v %v %v %v, want all true", ingest, edge, storage, processing)
	}
	if cfg.RotateNodeIdentityRequested() || cfg.GRPCInsecureAllowed() {
		t.Error("identity rotation and insecure gRPC must default to false")
	}
	if cfg.StorageCapacity() != 0 || cfg.MaxTranscodeSlots() != 0 || cfg.BandwidthLimitBytesPerSecond() != 0 {
		t.Error("capacity, transcode slots, and bandwidth limit must default to 0")
	}
}

func TestHelmsmanReportsEveryMissingRequiredKey(t *testing.T) {
	_, err := config.Load[Helmsman](config.Options{Service: ServiceID, Lookup: lookupFrom(nil)})
	var loadErr *config.LoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("Load error = %v, want *config.LoadError", err)
	}
	want := []string{"NODE_ID", "FOGHORN_CONTROL_ADDR", "MISTSERVER_URL", "EDGE_PUBLIC_URL", "HELMSMAN_STATE_DIR"}
	if !slices.Equal(loadErr.Missing, want) {
		t.Fatalf("missing = %v, want %v", loadErr.Missing, want)
	}
	if len(loadErr.Invalid) != 0 {
		t.Fatalf("invalid = %v, want none", loadErr.Invalid)
	}
}

func TestHelmsmanMalformedValuesKeepLegacyFallbacks(t *testing.T) {
	cfg := mustLoad(t, map[string]string{
		"HELMSMAN_BLOCKING_GRACE_MS":      "soon",
		"HELMSMAN_CAP_INGEST":             "yes",
		"HELMSMAN_CAP_EDGE":               "0",
		"HELMSMAN_ROTATE_NODE_IDENTITY":   "maybe",
		"GRPC_ALLOW_INSECURE":             "sometimes",
		"HELMSMAN_FREEZE_THRESHOLD":       "high",
		"HELMSMAN_TARGET_AFTER_FREEZE":    "0.5",
		"HELMSMAN_STORAGE_CAPACITY_BYTES": "-1",
		"HELMSMAN_MAX_TRANSCODES":         "four",
		"HELMSMAN_BW_LIMIT_BYTES_PER_SEC": "fast",
		"HELMSMAN_BW_LIMIT_MBPS":          "8",
	})
	if got := cfg.BlockingGrace(); got != 2000 {
		t.Errorf("BlockingGrace() = %d, want fallback 2000", got)
	}
	if ingest, edge, _, _ := cfg.Capabilities(); !ingest || edge {
		t.Errorf("Capabilities() ingest=%v edge=%v, want true/false", ingest, edge)
	}
	if cfg.RotateNodeIdentityRequested() || cfg.GRPCInsecureAllowed() {
		t.Error("non-boolean rotation and insecure flags must count as false")
	}
	if freeze, target := cfg.StorageThresholds(); freeze != 0 || target != 0.5 {
		t.Errorf("StorageThresholds() = %v/%v, want 0/0.5", freeze, target)
	}
	if cfg.StorageCapacity() != 0 || cfg.MaxTranscodeSlots() != 0 {
		t.Error("malformed capacity and transcode slots must count as 0")
	}
	if got := cfg.BandwidthLimitBytesPerSecond(); got != 1_000_000 {
		t.Errorf("BandwidthLimitBytesPerSecond() = %d, want 1000000 from HELMSMAN_BW_LIMIT_MBPS", got)
	}
}

func TestBandwidthLimitPrecedence(t *testing.T) {
	cases := []struct {
		name    string
		runtime EdgeRuntime
		want    uint64
	}{
		{"bytes per second wins", EdgeRuntime{BandwidthLimitBytesPerSec: "250000000", BandwidthLimitBytes: "1", BandwidthLimitMbps: "1"}, 250000000},
		{"bytes alias when per second is 0", EdgeRuntime{BandwidthLimitBytesPerSec: "0", BandwidthLimitBytes: "4096"}, 4096},
		{"megabits last", EdgeRuntime{BandwidthLimitMbps: "1000"}, 125000000},
		{"none", EdgeRuntime{}, 0},
	}
	for _, tc := range cases {
		if got := tc.runtime.BandwidthLimitBytesPerSecond(); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestHelmsmanValidateRequiresLoopbackManagementBind(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "::1", "[::1]", "localhost"} {
		if _, err := loadWith(map[string]string{"HELMSMAN_MANAGEMENT_BIND_ADDR": addr}); err != nil {
			t.Errorf("management bind %q rejected: %v", addr, err)
		}
	}
	_, err := loadWith(map[string]string{"HELMSMAN_MANAGEMENT_BIND_ADDR": "0.0.0.0"})
	if err == nil || !strings.Contains(err.Error(), "HELMSMAN_MANAGEMENT_BIND_ADDR") {
		t.Fatalf("non-loopback management bind error = %v, want a HELMSMAN_MANAGEMENT_BIND_ADDR error", err)
	}
}

func TestIsLoopbackBind(t *testing.T) {
	for _, addr := range []string{"127.0.0.1", "::1", "[::1]", "localhost"} {
		if !IsLoopbackBind(addr) {
			t.Errorf("IsLoopbackBind(%q) = false, want true", addr)
		}
	}
	for _, addr := range []string{"", "0.0.0.0", "::", "10.0.0.2", "helmsman"} {
		if IsLoopbackBind(addr) {
			t.Errorf("IsLoopbackBind(%q) = true, want false", addr)
		}
	}
}

func TestLegacyFallbacksMatchTagDefaults(t *testing.T) {
	tagDefault := func(field string) string {
		t.Helper()
		sf, ok := reflect.TypeFor[Helmsman]().FieldByName(field)
		if !ok {
			t.Fatalf("field %s not found", field)
		}
		spec, _, err := config.ParseFieldTag(string(sf.Tag))
		if err != nil {
			t.Fatalf("field %s: %v", field, err)
		}
		return spec.Default
	}
	checks := map[string]string{
		"BlockingGraceMs":    strconv.Itoa(fallbackBlockingGraceMs),
		"RotateNodeIdentity": strconv.FormatBool(fallbackRotateNodeIdentity),
		"GRPCAllowInsecure":  strconv.FormatBool(fallbackGRPCAllowInsecure),
		"CapIngest":          strconv.FormatBool(fallbackCapabilityEnabled),
		"CapEdge":            strconv.FormatBool(fallbackCapabilityEnabled),
		"CapStorage":         strconv.FormatBool(fallbackCapabilityEnabled),
		"CapProcessing":      strconv.FormatBool(fallbackCapabilityEnabled),
	}
	for field, want := range checks {
		if got := tagDefault(field); got != want {
			t.Errorf("%s tag default = %q, legacy fallback = %q", field, got, want)
		}
	}
}

func TestComponentVersionEnvCoversEveryVersionKey(t *testing.T) {
	values := map[string]string{}
	rt := reflect.TypeFor[EdgeRuntime]()
	for i := range rt.NumField() {
		spec, isConfig, err := config.ParseFieldTag(string(rt.Field(i).Tag))
		if err != nil || !isConfig {
			t.Fatalf("field %s: config=%v err=%v", rt.Field(i).Name, isConfig, err)
		}
		if strings.HasSuffix(spec.Env, "_VERSION") {
			values[spec.Env] = "v-" + spec.Env
		}
	}
	loaded, err := config.Load[EdgeRuntime](config.Options{Service: ServiceID, Lookup: lookupFrom(values)})
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range values {
		if got := loaded.ComponentVersionEnv(key); got != want {
			t.Errorf("ComponentVersionEnv(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestInstalledConfigFollowsReloadAndKeepsSnapshotOnFailure(t *testing.T) {
	for key, value := range requiredValues() {
		t.Setenv(key, value)
	}
	t.Setenv("HELMSMAN_SUPERVISOR", "s6")
	opts := config.Options{Service: ServiceID}
	cfg, err := config.Load[Helmsman](opts)
	if err != nil {
		t.Fatal(err)
	}
	live := config.NewLive(cfg, opts)
	installForTest(t, live)

	t.Setenv("HELMSMAN_SUPERVISOR", "systemd")
	t.Setenv("NODE_ID", "edge-2")
	if Runtime().Supervisor != "s6" || NodeID() != "edge-1" {
		t.Fatalf("installed snapshot changed before reload: supervisor=%q node=%q", Runtime().Supervisor, NodeID())
	}

	if err := live.Reload(); err != nil {
		t.Fatal(err)
	}
	if Runtime().Supervisor != "systemd" || NodeID() != "edge-2" {
		t.Fatalf("reload not observed: supervisor=%q node=%q", Runtime().Supervisor, NodeID())
	}

	t.Setenv("NODE_ID", "")
	if err := live.Reload(); err == nil {
		t.Fatal("reload without NODE_ID succeeded")
	}
	if NodeID() != "edge-2" {
		t.Fatalf("failed reload replaced the snapshot: node=%q", NodeID())
	}
}

// The accessors read only an installed configuration; the process
// environment is never consulted, so a read before Install panics.
func TestUninstalledAccessorsPanic(t *testing.T) {
	installForTest(t, nil)
	t.Setenv("NODE_ID", "edge-9")

	if Current() != nil {
		t.Fatal("Current() returned a configuration while none is installed")
	}
	for name, read := range map[string]func(){
		"Runtime":       func() { Runtime() },
		"NodeID":        func() { NodeID() },
		"MistServerURL": func() { MistServerURL() },
		"EdgePublicURL": func() { EdgePublicURL() },
		"StateDir":      func() { StateDir() },
		"MistClient":    func() { MistClient() },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s() without an installed configuration did not panic", name)
				}
			}()
			read()
		}()
	}
}

func TestScrubEdgeCredentialsVariantHasNoRequiredKeys(t *testing.T) {
	if _, err := config.Load[HelmsmanScrubEdgeCredentials](config.Options{Service: ServiceID, Lookup: lookupFrom(nil)}); err != nil {
		t.Fatalf("scrub-edge-credentials load with no environment: %v", err)
	}
}

func TestMistClientFollowsInstalledConfiguration(t *testing.T) {
	for key, value := range requiredValues() {
		t.Setenv(key, value)
	}
	t.Setenv("MIST_API_USERNAME", "")
	t.Setenv("MIST_API_PASSWORD", "")
	opts := config.Options{Service: ServiceID}
	cfg, err := config.Load[Helmsman](opts)
	if err != nil {
		t.Fatal(err)
	}
	live := config.NewLive(cfg, opts)
	installForTest(t, live)

	if got := MistClient(); got.BaseURL != "http://localhost:4242" || got.Username != "test" || got.Password != "test" {
		t.Fatalf("MistClient() defaults = %+v", got)
	}

	t.Setenv("MISTSERVER_URL", "http://mist:4242")
	t.Setenv("MIST_API_USERNAME", "frameworks")
	t.Setenv("MIST_API_PASSWORD", "rotated")
	if err := live.Reload(); err != nil {
		t.Fatal(err)
	}
	if got := MistClient(); got.BaseURL != "http://mist:4242" || got.Username != "frameworks" || got.Password != "rotated" {
		t.Fatalf("MistClient() after reload = %+v", got)
	}
}
