package bunny

import (
	"strings"

	"frameworks/pkg/config"
)

type Config struct {
	APIKey  string
	BaseURL string
}

func LoadConfig() *Config {
	apiKey := strings.TrimSpace(config.GetEnv("BUNNY_API_KEY", ""))
	if apiKey == "" {
		return nil
	}
	baseURL := strings.TrimSpace(config.GetEnv("BUNNY_API_BASE_URL", ""))
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Config{
		APIKey:  apiKey,
		BaseURL: strings.TrimRight(baseURL, "/"),
	}
}

func NewClientFromConfig(cfg *Config) *Client {
	if cfg == nil {
		return nil
	}
	client := NewClient(cfg.APIKey)
	client.baseURL = cfg.BaseURL
	return client
}
