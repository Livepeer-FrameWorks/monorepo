package appconfig

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func loadChandler(t *testing.T, values map[string]string) *Chandler {
	t.Helper()
	cfg, err := config.Load[Chandler](config.Options{
		Service: "chandler",
		Lookup: func(key string) (string, bool) {
			v, ok := values[key]
			return v, ok
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func TestChandlerDefaults(t *testing.T) {
	cfg := loadChandler(t, nil)
	if cfg.Port != "18020" {
		t.Fatalf("PORT default = %q, want 18020", cfg.Port)
	}
	if cfg.S3Region != "us-east-1" || cfg.CacheMaxBytes != 50*1024*1024 || cfg.CacheTTLSeconds != 30 {
		t.Fatalf("storage/cache defaults = %q %d %d", cfg.S3Region, cfg.CacheMaxBytes, cfg.CacheTTLSeconds)
	}
	if cfg.AdvertiseHost != "chandler" || cfg.ServiceToken != "" || cfg.DevAllowEnvS3 {
		t.Fatalf("identity defaults = %q token-set=%v dev=%v", cfg.AdvertiseHost, cfg.ServiceToken != "", cfg.DevAllowEnvS3)
	}
}

func TestChandlerQuartermasterAddr(t *testing.T) {
	if got := loadChandler(t, nil).QuartermasterAddr(); got != "quartermaster:19002" {
		t.Fatalf("default address = %q", got)
	}
	if got := loadChandler(t, map[string]string{"QUARTERMASTER_HOST": "qm.internal", "QUARTERMASTER_GRPC_PORT": "29002"}).QuartermasterAddr(); got != "qm.internal:29002" {
		t.Fatalf("host/port address = %q", got)
	}
	explicit := map[string]string{"QUARTERMASTER_GRPC_ADDR": "qm:1", "QUARTERMASTER_HOST": "ignored"}
	if got := loadChandler(t, explicit).QuartermasterAddr(); got != "qm:1" {
		t.Fatalf("explicit address = %q", got)
	}
}
