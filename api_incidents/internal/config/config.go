// Package config loads Lookout's process configuration from the environment.
package config

import (
	"errors"
	"fmt"
	"strings"

	pkgconfig "github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

// Env keys whose values are read on every use so an env-file reload takes
// effect without a restart.
const (
	EnvAlertmanagerToken  = "LOOKOUT_ALERTMANAGER_TOKEN"
	EnvNotifyEmailTo      = "LOOKOUT_NOTIFY_EMAIL_TO"
	EnvSlackWebhookURL    = "LOOKOUT_SLACK_WEBHOOK_URL"
	EnvDiscordWebhookURL  = "LOOKOUT_DISCORD_WEBHOOK_URL"
	EnvWebappPublicURL    = "WEBAPP_PUBLIC_URL"
	defaultHTTPPort       = "18022"
	defaultGRPCPort       = "19008"
	defaultAdvertiseHost  = "lookout"
	defaultKafkaClusterID = "local"
)

// Config holds the settings Lookout reads once at startup.
type Config struct {
	HTTPPort              string
	GRPCPort              string
	AdvertiseHost         string
	DatabaseURL           string
	ServiceToken          string
	JWTSecret             string
	QuartermasterGRPCAddr string
	DecklogGRPCAddr       string
	KafkaBrokers          []string
	KafkaClusterID        string
	IncidentsTopic        string
	// ServiceEventsTopic is the aggregator's local service_events topic. Central
	// control-plane and marketing producers publish there, so mirrored regional
	// copies are not read.
	ServiceEventsTopic string
	ClusterID          string
	NodeID             string
	Region             string
	GRPCAllowInsecure  bool
	GRPCTLSCAPath      string
	GRPCTLSCertPath    string
	GRPCTLSKeyPath     string
}

// Load reads and validates the startup configuration.
func Load() (Config, error) {
	cfg := Config{
		HTTPPort:              strings.TrimSpace(pkgconfig.GetEnv("LOOKOUT_PORT", defaultHTTPPort)),
		GRPCPort:              strings.TrimSpace(pkgconfig.GetEnv("LOOKOUT_GRPC_PORT", defaultGRPCPort)),
		AdvertiseHost:         strings.TrimSpace(pkgconfig.GetEnv("LOOKOUT_HOST", defaultAdvertiseHost)),
		DatabaseURL:           strings.TrimSpace(pkgconfig.GetEnv("DATABASE_URL", "")),
		ServiceToken:          strings.TrimSpace(pkgconfig.GetEnv("SERVICE_TOKEN", "")),
		JWTSecret:             pkgconfig.GetEnv("JWT_SECRET", ""),
		QuartermasterGRPCAddr: strings.TrimSpace(pkgconfig.GetEnv("QUARTERMASTER_GRPC_ADDR", "")),
		DecklogGRPCAddr:       strings.TrimSpace(pkgconfig.GetEnv("DECKLOG_GRPC_ADDR", "")),
		KafkaBrokers:          splitList(pkgconfig.GetEnv("KAFKA_BROKERS", "")),
		KafkaClusterID:        strings.TrimSpace(pkgconfig.GetEnv("KAFKA_CLUSTER_ID", defaultKafkaClusterID)),
		IncidentsTopic:        topology.TopicLookoutIncidents,
		ServiceEventsTopic:    topology.TopicServiceEvents,
		ClusterID:             strings.TrimSpace(pkgconfig.GetEnv("CLUSTER_ID", "")),
		NodeID:                strings.TrimSpace(pkgconfig.GetEnv("NODE_ID", "")),
		Region:                strings.TrimSpace(pkgconfig.GetEnv("REGION", "")),
		GRPCAllowInsecure:     pkgconfig.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		GRPCTLSCAPath:         strings.TrimSpace(pkgconfig.GetEnv("GRPC_TLS_CA_PATH", "")),
		GRPCTLSCertPath:       strings.TrimSpace(pkgconfig.GetEnv("GRPC_TLS_CERT_PATH", "")),
		GRPCTLSKeyPath:        strings.TrimSpace(pkgconfig.GetEnv("GRPC_TLS_KEY_PATH", "")),
	}
	var missing []string
	for key, value := range map[string]string{
		"DATABASE_URL":            cfg.DatabaseURL,
		"SERVICE_TOKEN":           cfg.ServiceToken,
		"QUARTERMASTER_GRPC_ADDR": cfg.QuartermasterGRPCAddr,
		"DECKLOG_GRPC_ADDR":       cfg.DecklogGRPCAddr,
		EnvAlertmanagerToken:      AlertmanagerToken(),
	} {
		if value == "" {
			missing = append(missing, key)
		}
	}
	if len(cfg.KafkaBrokers) == 0 {
		missing = append(missing, "KAFKA_BROKERS")
	}
	if len(missing) > 0 {
		return Config{}, fmt.Errorf("missing required environment: %s", strings.Join(sortedCopy(missing), ", "))
	}
	if cfg.HTTPPort == "" || cfg.GRPCPort == "" {
		return Config{}, errors.New("LOOKOUT_PORT and LOOKOUT_GRPC_PORT must not be empty")
	}
	return cfg, nil
}

// AlertmanagerToken returns the bearer token Alertmanager must present.
func AlertmanagerToken() string {
	return strings.TrimSpace(pkgconfig.GetEnv(EnvAlertmanagerToken, ""))
}

// NotifyEmailRecipients returns the operator email recipients, if configured.
func NotifyEmailRecipients() []string {
	return splitList(pkgconfig.GetEnv(EnvNotifyEmailTo, ""))
}

// SlackWebhookURL returns the Slack incoming webhook URL, if configured.
func SlackWebhookURL() string {
	return strings.TrimSpace(pkgconfig.GetEnv(EnvSlackWebhookURL, ""))
}

// DiscordWebhookURL returns the Discord webhook URL, if configured.
func DiscordWebhookURL() string {
	return strings.TrimSpace(pkgconfig.GetEnv(EnvDiscordWebhookURL, ""))
}

// WebappPublicURL returns the public webapp base URL without a trailing slash.
func WebappPublicURL() string {
	return strings.TrimRight(strings.TrimSpace(pkgconfig.GetEnv(EnvWebappPublicURL, "")), "/")
}

func splitList(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}
