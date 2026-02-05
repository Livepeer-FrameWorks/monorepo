package llm

import (
	"fmt"
	"strings"

	"frameworks/pkg/config"
)

type Config struct {
	Provider string
	Model    string
	APIKey   string
	APIURL   string
}

func LoadConfig() Config {
	return Config{
		Provider: config.GetEnv("LLM_PROVIDER", "openai"),
		Model:    config.GetEnv("LLM_MODEL", ""),
		APIKey:   config.GetEnv("LLM_API_KEY", ""),
		APIURL:   config.GetEnv("LLM_API_URL", ""),
	}
}

func NewProvider(cfg Config) (Provider, error) {
	switch strings.ToLower(cfg.Provider) {
	case "openai":
		return NewOpenAIProvider(cfg), nil
	case "anthropic":
		return NewAnthropicProvider(cfg), nil
	case "ollama":
		return NewOllamaProvider(cfg), nil
	default:
		return nil, fmt.Errorf("unknown LLM provider %q", cfg.Provider)
	}
}
