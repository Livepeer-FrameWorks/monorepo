package appconfig

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

func lookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
}

func requiredValues() map[string]string {
	return map[string]string{
		"DATABASE_URL":  "postgres://skipper@db/skipper",
		"JWT_SECRET":    "jwt",
		"SERVICE_TOKEN": "token",
	}
}

func loadSkipper(t *testing.T, values map[string]string) (*Skipper, error) {
	t.Helper()
	return config.Load[Skipper](config.Options{Service: "skipper", Lookup: lookup(values)})
}

func TestSkipperRequiresDatabaseAndCredentials(t *testing.T) {
	_, err := loadSkipper(t, map[string]string{})
	var loadErr *config.LoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("Load error = %v, want LoadError", err)
	}
	for _, key := range []string{"DATABASE_URL", "JWT_SECRET", "SERVICE_TOKEN"} {
		if !slices.Contains(loadErr.Missing, key) {
			t.Errorf("missing keys %v do not include %s", loadErr.Missing, key)
		}
	}
}

func TestSkipperDefaults(t *testing.T) {
	cfg, err := loadSkipper(t, requiredValues())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HTTPListen.Port != "18018" || cfg.GRPCListen.Port != "19007" {
		t.Errorf("ports = %s/%s, want 18018/19007", cfg.HTTPListen.Port, cfg.GRPCListen.Port)
	}
	if cfg.LLMMaxTokens != 4096 || cfg.RequiredTierLevel != 3 || cfg.SearchLimit != 8 || cfg.MaxHistoryMessages != 20 {
		t.Errorf("unexpected numeric defaults: %+v", cfg)
	}
	if cfg.CrawlInterval != 24*time.Hour || cfg.SocialInterval != 2*time.Hour {
		t.Errorf("intervals = %s/%s, want 24h/2h", cfg.CrawlInterval, cfg.SocialInterval)
	}
	if !cfg.WebUIEnabled || cfg.WebUIInsecure || !cfg.NotifyWebsocket || !cfg.NotifyMCP || cfg.NotifyEmail {
		t.Errorf("unexpected boolean defaults: %+v", cfg)
	}
	if cfg.SMTPPort != "587" || cfg.FromEmail != "noreply@frameworks.network" || cfg.AdvertiseHost != "skipper" {
		t.Errorf("unexpected string defaults: %+v", cfg)
	}
	if d, err := cfg.HeartbeatDuration(); err != nil || d != 30*time.Minute {
		t.Errorf("HeartbeatDuration = %s, %v; want 30m", d, err)
	}
}

func TestSkipperConsultantFallsBackToLLMSettings(t *testing.T) {
	values := requiredValues()
	values["LLM_PROVIDER"] = "anthropic"
	values["LLM_MODEL"] = "claude"
	values["LLM_API_KEY"] = "llm-key"
	values["LLM_API_URL"] = "https://llm.example"
	values["EMBEDDING_PROVIDER"] = "openai"
	values["RERANKER_PROVIDER"] = "cohere"
	values["SKIPPER_CHAT_RATE_LIMIT_OVERRIDES"] = "t1:5,bad"
	cfg, err := loadSkipper(t, values)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := cfg.Consultant()
	if err != nil {
		t.Fatalf("Consultant: %v", err)
	}
	if got.EmbeddingProvider != "openai" || got.EmbeddingModel != "claude" || got.EmbeddingAPIKey != "llm-key" || got.EmbeddingAPIURL != "https://llm.example" {
		t.Errorf("embedding settings = %q %q %q %q", got.EmbeddingProvider, got.EmbeddingModel, got.EmbeddingAPIKey, got.EmbeddingAPIURL)
	}
	if got.UtilityLLMProvider != "anthropic" || got.UtilityLLMAPIKey != "llm-key" {
		t.Errorf("utility settings = %q %q", got.UtilityLLMProvider, got.UtilityLLMAPIKey)
	}
	if got.RerankAPIKey != "llm-key" || got.RerankAPIURL != "" {
		t.Errorf("reranker key/url = %q %q; want LLM key and no URL fallback", got.RerankAPIKey, got.RerankAPIURL)
	}
	if len(got.RateLimitOverrides) != 1 || got.RateLimitOverrides["t1"] != 5 {
		t.Errorf("RateLimitOverrides = %v", got.RateLimitOverrides)
	}
}

func TestSkipperParsesOriginRewrites(t *testing.T) {
	values := requiredValues()
	values["SKIPPER_CRAWL_ORIGIN_REWRITES"] = `{"http://localhost:18090":"http://nginx"}`
	cfg, err := loadSkipper(t, values)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got, err := cfg.Consultant()
	if err != nil {
		t.Fatalf("Consultant: %v", err)
	}
	if got.CrawlOriginRewrites["http://localhost:18090"] != "http://nginx" {
		t.Errorf("CrawlOriginRewrites = %v", got.CrawlOriginRewrites)
	}
}

func TestSkipperValidateRejectsInvalidOriginRewrites(t *testing.T) {
	for name, raw := range map[string]string{
		"malformed json": `{"http://localhost:18090"`,
		"path in origin": `{"http://localhost:18090/docs":"http://nginx"}`,
	} {
		t.Run(name, func(t *testing.T) {
			values := requiredValues()
			values["SKIPPER_CRAWL_ORIGIN_REWRITES"] = raw
			_, err := loadSkipper(t, values)
			if err == nil || !strings.Contains(err.Error(), "SKIPPER_CRAWL_ORIGIN_REWRITES") {
				t.Fatalf("Load error = %v, want SKIPPER_CRAWL_ORIGIN_REWRITES rejection", err)
			}
		})
	}
}

func TestSkipperHeartbeatIntervalInvalidIsReportedNotFatal(t *testing.T) {
	values := requiredValues()
	values["HEARTBEAT_INTERVAL"] = "soon"
	cfg, err := loadSkipper(t, values)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, err := cfg.HeartbeatDuration(); err == nil {
		t.Fatal("HeartbeatDuration accepted an invalid interval")
	}
}

func TestSkipperRejectsUnparsableTypedValues(t *testing.T) {
	values := requiredValues()
	values["CRAWL_INTERVAL"] = "daily"
	values["SKIPPER_WEB_UI"] = "maybe"
	_, err := loadSkipper(t, values)
	var loadErr *config.LoadError
	if !errors.As(err, &loadErr) || len(loadErr.Invalid) != 2 {
		t.Fatalf("Load error = %v, want two invalid keys", err)
	}
}
