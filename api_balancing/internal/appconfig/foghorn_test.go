package appconfig

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func loadFoghorn(t *testing.T, env map[string]string) (*Foghorn, error) {
	t.Helper()
	return config.Load[Foghorn](config.Options{
		Service: "foghorn",
		Lookup: func(key string) (string, bool) {
			value, ok := env[key]
			return value, ok
		},
	})
}

func requiredFoghornEnv() map[string]string {
	return map[string]string{
		"SERVICE_TOKEN":                      "service-token",
		"DATABASE_URL":                       "postgres://foghorn@db/foghorn",
		"FOGHORN_BALANCER_CAPABILITY_SECRET": "capability-secret",
		"FOGHORN_STATE_ENCRYPTION_KEY":       "state-key",
	}
}

func TestLoadFoghornDefaults(t *testing.T) {
	cfg, err := loadFoghorn(t, requiredFoghornEnv())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	checks := map[string][2]any{
		"PORT":                                     {cfg.Port, "18008"},
		"FOGHORN_INTERNAL_HTTP_PORT":               {cfg.InternalHTTPPort, "18027"},
		"FOGHORN_INTERNAL_HTTP_BIND_ADDR":          {cfg.InternalHTTPBindAddr, "127.0.0.1"},
		"FOGHORN_PUBLIC_HTTP_BIND_ADDR":            {cfg.PublicHTTPBindAddr, ""},
		"FOGHORN_INTERNAL_GRPC_BIND_ADDR":          {cfg.InternalGRPCBindAddr, ":18019"},
		"FOGHORN_EXTERNAL_GRPC_BIND_ADDR":          {cfg.ExternalGRPCBindAddr, ":18029"},
		"DECKLOG_GRPC_ADDR":                        {cfg.DecklogGRPCAddr, "decklog:18006"},
		"QUARTERMASTER_GRPC_ADDR":                  {cfg.QuartermasterGRPCAddr, "quartermaster:19002"},
		"COMMODORE_GRPC_ADDR":                      {cfg.CommodoreGRPCAddr, "commodore:19001"},
		"PURSER_GRPC_ADDR":                         {cfg.PurserGRPCAddr, "purser:19003"},
		"COMMODORE_CACHE_TTL":                      {cfg.CommodoreCacheTTL, 60 * time.Second},
		"GEOIP_CACHE_TTL":                          {cfg.GeoIPCacheTTL, 300 * time.Second},
		"STORAGE_S3_REGION":                        {cfg.S3Region, "us-east-1"},
		"CPU_WEIGHT":                               {cfg.CPUWeight, 500},
		"BANDWIDTH_WEIGHT":                         {cfg.BandwidthWeight, 1000},
		"STREAM_BONUS":                             {cfg.StreamBonus, 50},
		"EDGE_RELEASE_RECONCILE_INTERVAL_SECONDS":  {cfg.EdgeReleaseReconcileIntervalSeconds, 60},
		"FOGHORN_RESTART_RECONNECT_WINDOW_SECONDS": {cfg.RestartReconnectWindowSeconds, 20},
		"BRAND_DOMAIN":                             {cfg.PlatformRootDomain, "frameworks.network"},
		"INGEST_RESOLVE_RATE_PER_MIN":              {cfg.IngestResolveRatePerMinute, "60"},
	}
	for key, pair := range checks {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("%s = %#v, want %#v", key, pair[0], pair[1])
		}
	}
	if cfg.LocalClusterID() != "default" || cfg.ControlCellID() != "default" {
		t.Errorf("unset cluster identity = (%q, %q), want default", cfg.LocalClusterID(), cfg.ControlCellID())
	}
}

func TestLoadFoghornRequiresSecrets(t *testing.T) {
	_, err := loadFoghorn(t, map[string]string{})
	var loadErr *config.LoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("err = %v, want LoadError", err)
	}
	want := []string{"DATABASE_URL", "SERVICE_TOKEN", "FOGHORN_BALANCER_CAPABILITY_SECRET", "FOGHORN_STATE_ENCRYPTION_KEY"}
	for _, key := range want {
		found := false
		for _, missing := range loadErr.Missing {
			found = found || missing == key
		}
		if !found {
			t.Errorf("missing keys %v do not include %s", loadErr.Missing, key)
		}
	}
}

func TestFoghornClusterIdentity(t *testing.T) {
	cfg := &Foghorn{ClusterID: "media-eu-1"}
	if cfg.ControlCellID() != "media-eu-1" {
		t.Fatalf("ControlCellID = %q, want the cluster ID", cfg.ControlCellID())
	}
	cfg.MediaAuthorityCellID = "cell-eu"
	if cfg.ControlCellID() != "cell-eu" || cfg.LocalClusterID() != "media-eu-1" {
		t.Fatalf("identity = (%q, %q)", cfg.LocalClusterID(), cfg.ControlCellID())
	}
}

func TestFoghornValidate(t *testing.T) {
	cases := map[string]struct {
		cfg     Foghorn
		wantErr string
	}{
		"empty configuration":        {cfg: Foghorn{}},
		"registered port in range":   {cfg: Foghorn{FoghornListeners: FoghornListeners{InternalGRPCPort: 18019}}},
		"grpc bind addrs valid":      {cfg: Foghorn{FoghornListeners: FoghornListeners{InternalGRPCBindAddr: ":18019", ExternalGRPCBindAddr: "0.0.0.0:18029"}}},
		"internal bind addr no port": {cfg: Foghorn{FoghornListeners: FoghornListeners{InternalGRPCBindAddr: "18019"}}, wantErr: "FOGHORN_INTERNAL_GRPC_BIND_ADDR"},
		"external bind addr no port": {cfg: Foghorn{FoghornListeners: FoghornListeners{ExternalGRPCBindAddr: "0.0.0.0:"}}, wantErr: "FOGHORN_EXTERNAL_GRPC_BIND_ADDR"},
		"registered port too large":  {cfg: Foghorn{FoghornListeners: FoghornListeners{InternalGRPCPort: 70000}}, wantErr: "FOGHORN_INTERNAL_GRPC_PORT"},
		"registered port negative":   {cfg: Foghorn{FoghornListeners: FoghornListeners{InternalGRPCPort: -1}}, wantErr: "FOGHORN_INTERNAL_GRPC_PORT"},
		"absolute storage base":      {cfg: Foghorn{FoghornStorage: FoghornStorage{DefaultStorageBase: "/var/lib/frameworks"}}},
		"relative storage base":      {cfg: Foghorn{FoghornStorage: FoghornStorage{DefaultStorageBase: "var/lib"}}, wantErr: "FOGHORN_DEFAULT_STORAGE_BASE"},
		"seal pair without trust":    {cfg: Foghorn{FoghornMediaAuthority: FoghornMediaAuthority{MediaAuthoritySealKeyID: "seal-1"}}},
		"trust set with cell":        {cfg: Foghorn{FoghornMediaAuthority: FoghornMediaAuthority{MediaAuthorityTrustSet: "{}", MediaAuthorityCellID: "cell"}}},
		"trust set without cell":     {cfg: Foghorn{FoghornMediaAuthority: FoghornMediaAuthority{MediaAuthorityTrustSet: "{}"}}, wantErr: "MEDIA_AUTHORITY_CELL_ID"},
		"trust set with seal key id": {cfg: Foghorn{FoghornMediaAuthority: FoghornMediaAuthority{MediaAuthorityTrustSet: "{}", MediaAuthorityCellID: "cell", MediaAuthoritySealKeyID: "seal-1"}}, wantErr: "configured together"},
		"trust set with seal key":    {cfg: Foghorn{FoghornMediaAuthority: FoghornMediaAuthority{MediaAuthorityTrustSet: "{}", MediaAuthorityCellID: "cell", MediaAuthoritySealPrivateKeyPEMB64: "cGVt"}}, wantErr: "configured together"},
		"trust set with seal pair":   {cfg: Foghorn{FoghornMediaAuthority: FoghornMediaAuthority{MediaAuthorityTrustSet: "{}", MediaAuthorityCellID: "cell", MediaAuthoritySealKeyID: "seal-1", MediaAuthoritySealPrivateKeyPEMB64: "cGVt"}}},
		"unknown metadata policy":    {cfg: Foghorn{MetadataPolicy: "permissive"}, wantErr: "GRPC_METADATA_POLICY"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if tc.cfg.MetadataPolicy == "" {
				tc.cfg.MetadataPolicy = "deny"
			}
			err := tc.cfg.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate() = %v, want error containing %q", err, tc.wantErr)
			}
		})
	}
}

func TestLoadFoghornRunsValidate(t *testing.T) {
	env := requiredFoghornEnv()
	env["FOGHORN_DEFAULT_STORAGE_BASE"] = "relative/path"
	if _, err := loadFoghorn(t, env); err == nil || !strings.Contains(err.Error(), "FOGHORN_DEFAULT_STORAGE_BASE") {
		t.Fatalf("load err = %v, want storage base rejection", err)
	}
}

func TestLoadFoghornDefaultsMetadataPolicyToDeny(t *testing.T) {
	cfg, err := loadFoghorn(t, requiredFoghornEnv())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MetadataPolicy != config.MetadataPolicyDeny {
		t.Fatalf("GRPC_METADATA_POLICY default = %q, want deny", cfg.MetadataPolicy)
	}
}

// FEDERATION_ENABLED keeps its v0.3.10 meaning: only the exact value "true"
// enables federation; every other value leaves it off without failing startup.
func TestFoghornFederationEnabledOnlyByExactTrue(t *testing.T) {
	for value, want := range map[string]bool{
		"true": true, "": false, "false": false, "TRUE": false, "True": false,
		"1": false, "t": false, "yes": false, "on": false, "enabled": false,
	} {
		env := requiredFoghornEnv()
		env["FEDERATION_ENABLED"] = value
		cfg, err := loadFoghorn(t, env)
		if err != nil {
			t.Errorf("FEDERATION_ENABLED=%q: load failed: %v", value, err)
			continue
		}
		if got := cfg.Federation(); got != want {
			t.Errorf("FEDERATION_ENABLED=%q: federation = %v, want %v", value, got, want)
		}
	}
}

func TestLoadFoghornDataMigrationsRequiresDatabaseURL(t *testing.T) {
	_, err := config.Load[FoghornDataMigrations](config.Options{Service: "foghorn", Lookup: func(string) (string, bool) { return "", false }})
	var loadErr *config.LoadError
	if !errors.As(err, &loadErr) || len(loadErr.Missing) != 1 || loadErr.Missing[0] != "DATABASE_URL" {
		t.Fatalf("err = %v, want missing DATABASE_URL", err)
	}
}

func TestCurrentFollowsInstalledSource(t *testing.T) {
	if Current() == nil {
		t.Fatal("Current must never return nil")
	}
	first := &Foghorn{ClusterID: "first"}
	restore := Install(func() *Foghorn { return first })
	if Current().ClusterID != "first" {
		t.Fatalf("Current().ClusterID = %q, want first", Current().ClusterID)
	}
	second := &Foghorn{ClusterID: "second"}
	restoreSecond := Install(func() *Foghorn { return second })
	if Current().ClusterID != "second" {
		t.Fatalf("Current().ClusterID = %q, want second", Current().ClusterID)
	}
	restoreSecond()
	if Current().ClusterID != "first" {
		t.Fatalf("restore did not reinstate the previous source: %q", Current().ClusterID)
	}
	restore()
	if Current().ClusterID != "" {
		t.Fatalf("restoring to no source must yield a zero configuration, got %q", Current().ClusterID)
	}
}
