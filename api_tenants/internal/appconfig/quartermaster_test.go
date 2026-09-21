package appconfig

import (
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

func loadQuartermaster(t *testing.T, overrides map[string]string) (*Quartermaster, error) {
	t.Helper()
	values := map[string]string{
		"SERVICE_TOKEN":                         "service-token",
		"JWT_SECRET":                            "jwt-secret",
		"DATABASE_URL":                          "postgres://quartermaster@localhost/quartermaster",
		"CLUSTER_ACCESS_MATERIALIZATION_SECRET": "materialization-secret",
		"USAGE_HASH_SECRET":                     "usage-hash-secret",
	}
	for key, value := range overrides {
		values[key] = value
	}
	return config.Load[Quartermaster](config.Options{
		Service: "quartermaster",
		Lookup: func(key string) (string, bool) {
			value, ok := values[key]
			return value, ok
		},
	})
}

func TestQuartermasterHealthPollerDefaults(t *testing.T) {
	cfg, err := loadQuartermaster(t, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HealthPollIntervalSeconds != 30 || cfg.HealthTimeoutMS != 2000 || cfg.HealthMaxConcurrency != 8 || cfg.HealthBatchSize != 200 {
		t.Fatalf("poll defaults = %d/%d/%d/%d", cfg.HealthPollIntervalSeconds, cfg.HealthTimeoutMS, cfg.HealthMaxConcurrency, cfg.HealthBatchSize)
	}
	if !cfg.HealthGRPCWatch || cfg.HealthWatchRefreshSeconds != 60 || cfg.HealthWatchBackoffSeconds != 300 || cfg.HealthWatchDialTimeoutMS != 2000 || cfg.HealthWatchMaxConcurrency != 0 {
		t.Fatalf("watch defaults = %v/%d/%d/%d/%d", cfg.HealthGRPCWatch, cfg.HealthWatchRefreshSeconds, cfg.HealthWatchBackoffSeconds, cfg.HealthWatchDialTimeoutMS, cfg.HealthWatchMaxConcurrency)
	}
	if got := cfg.HealthMinAge(); got != -1 {
		t.Fatalf("HealthMinAge() unset = %d, want -1", got)
	}
}

func TestQuartermasterHealthMinAge(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{raw: "0", want: 0},
		{raw: "45", want: 45},
		{raw: "-5", want: -5},
	} {
		cfg, err := loadQuartermaster(t, map[string]string{"QM_HEALTH_MIN_AGE_SECONDS": tc.raw})
		if err != nil {
			t.Fatalf("Load(%q): %v", tc.raw, err)
		}
		if got := cfg.HealthMinAge(); got != tc.want {
			t.Fatalf("HealthMinAge(%q) = %d, want %d", tc.raw, got, tc.want)
		}
	}
}

func TestQuartermasterValidateHealthPoller(t *testing.T) {
	for _, tc := range []struct {
		name      string
		overrides map[string]string
		wantErr   string
	}{
		{name: "zero poll interval", overrides: map[string]string{"QM_HEALTH_POLL_INTERVAL_SECONDS": "0"}, wantErr: "QM_HEALTH_POLL_INTERVAL_SECONDS"},
		{name: "negative poll interval", overrides: map[string]string{"QM_HEALTH_POLL_INTERVAL_SECONDS": "-1"}, wantErr: "QM_HEALTH_POLL_INTERVAL_SECONDS"},
		{name: "zero watch refresh", overrides: map[string]string{"QM_HEALTH_WATCH_REFRESH_SECONDS": "0"}, wantErr: "QM_HEALTH_WATCH_REFRESH_SECONDS"},
		{name: "zero watch refresh with watch disabled", overrides: map[string]string{"QM_HEALTH_WATCH_REFRESH_SECONDS": "0", "QM_HEALTH_GRPC_WATCH": "false"}},
		{name: "non-integer min age", overrides: map[string]string{"QM_HEALTH_MIN_AGE_SECONDS": "soon"}, wantErr: "QM_HEALTH_MIN_AGE_SECONDS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadQuartermaster(t, tc.overrides)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Load: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Load error = %v, want mention of %s", err, tc.wantErr)
			}
		})
	}
}

// Every service with a gRPC endpoint in pkg/servicedefs can override the TLS
// name its health watch validates, through <SERVICE>_GRPC_TLS_SERVER_NAME.
// A new gRPC service without a declared key fails here.
func TestQuartermasterHealthWatchTLSServerNameCoversEveryGRPCService(t *testing.T) {
	overrides := map[string]string{}
	want := map[string]string{}
	for _, svc := range servicedefs.GRPCServices() {
		key := strings.ToUpper(strings.ReplaceAll(svc.ServiceID, "-", "_")) + "_GRPC_TLS_SERVER_NAME"
		overrides[key] = svc.ServiceID + ".tls.example"
		want[svc.ServiceID] = svc.ServiceID + ".tls.example"
	}
	cfg, err := loadQuartermaster(t, overrides)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for serviceID, name := range want {
		if got := cfg.HealthWatchTLSServerName(serviceID); got != name {
			t.Errorf("HealthWatchTLSServerName(%q) = %q, want %q: declare %s_GRPC_TLS_SERVER_NAME on Quartermaster", serviceID, got, name, strings.ToUpper(strings.ReplaceAll(serviceID, "-", "_")))
		}
	}
}

func TestQuartermasterHealthWatchTLSServerName(t *testing.T) {
	cfg, err := loadQuartermaster(t, map[string]string{
		"FOGHORN_GRPC_TLS_SERVER_NAME":         "foghorn.example",
		"PERISCOPE_QUERY_GRPC_TLS_SERVER_NAME": "periscope.example",
		"DECKLOG_GRPC_TLS_SERVER_NAME":         "decklog.example",
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for serviceType, want := range map[string]string{
		"foghorn":          "foghorn.example",
		" periscope-query": "periscope.example",
		"decklog":          "decklog.example",
		"signalman":        "",
		"livepeer-gateway": "",
	} {
		if got := cfg.HealthWatchTLSServerName(serviceType); got != want {
			t.Fatalf("HealthWatchTLSServerName(%q) = %q, want %q", serviceType, got, want)
		}
	}
}
