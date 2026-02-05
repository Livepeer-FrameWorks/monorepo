package config

import "frameworks/pkg/config"

// Config stores environment configuration for Skipper.
type Config struct {
	Port           string
	GRPCPort       string
	DatabaseURL    string
	LLMProvider    string
	LLMModel       string
	LLMAPIKey      string
	LLMAPIURL      string
	SearchProvider string
	SearchAPIKey   string
	SearchAPIURL   string
}

// LoadConfig loads the Skipper configuration from environment variables.
func LoadConfig() Config {
	return Config{
		Port:           config.GetEnv("PORT", "18016"),
		GRPCPort:       config.GetEnv("GRPC_PORT", "19016"),
		DatabaseURL:    config.RequireEnv("DATABASE_URL"),
		LLMProvider:    config.GetEnv("LLM_PROVIDER", ""),
		LLMModel:       config.GetEnv("LLM_MODEL", ""),
		LLMAPIKey:      config.GetEnv("LLM_API_KEY", ""),
		LLMAPIURL:      config.GetEnv("LLM_API_URL", ""),
		SearchProvider: config.GetEnv("SEARCH_PROVIDER", ""),
		SearchAPIKey:   config.GetEnv("SEARCH_API_KEY", ""),
		SearchAPIURL:   config.GetEnv("SEARCH_API_URL", ""),
	}
}
