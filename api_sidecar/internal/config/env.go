package config

import (
	"strconv"

	"frameworks/pkg/config"
)

// HelmsmanConfig holds all configuration for the Helmsman sidecar.
// Required vars will cause the service to fail at startup if missing.
// Optional vars have sensible defaults or disable features when empty.
type HelmsmanConfig struct {
	// Required - service identity
	NodeID             string
	FoghornControlAddr string

	// MistServer connection
	MistServerURL   string
	MistPassword    string // Prometheus scrape auth
	MistAPIUsername string
	MistAPIPassword string

	// Foghorn URL for balance source in MistServer config
	FoghornURL string

	// Storage configuration
	StorageLocalPath     string
	StorageS3Bucket      string
	StorageS3Prefix      string
	StorageCapacityBytes uint64

	// Capabilities (all default to true)
	CapIngest     bool
	CapEdge       bool
	CapStorage    bool
	CapProcessing bool

	// Limits
	MaxTranscodes int

	// Edge node configuration
	EdgePublicHostname string
	EnrollmentToken    string

	// Webhook URL for MistServer triggers
	WebhookURL string

	// gRPC TLS configuration
	GRPCUseTLS      bool
	GRPCTLSCertPath string
	GRPCTLSKeyPath  string
}

// LoadHelmsmanConfig loads configuration from environment variables.
// Call this after config.LoadEnv() has been called.
func LoadHelmsmanConfig() *HelmsmanConfig {
	return &HelmsmanConfig{
		// Required
		NodeID:             config.RequireEnv("NODE_ID"),
		FoghornControlAddr: config.RequireEnv("FOGHORN_CONTROL_ADDR"),

		// MistServer (required for health checks)
		MistServerURL:   config.RequireEnv("MISTSERVER_URL"),
		MistPassword:    config.GetEnv("MIST_PASSWORD", ""),
		MistAPIUsername: config.GetEnv("MIST_API_USERNAME", ""),
		MistAPIPassword: config.GetEnv("MIST_API_PASSWORD", ""),

		// Foghorn URL for balance source
		FoghornURL: config.RequireEnv("FOGHORN_URL"),

		// Storage (optional - empty disables local storage features)
		StorageLocalPath:     config.GetEnv("HELMSMAN_STORAGE_LOCAL_PATH", ""),
		StorageS3Bucket:      config.GetEnv("HELMSMAN_STORAGE_S3_BUCKET", ""),
		StorageS3Prefix:      config.GetEnv("HELMSMAN_STORAGE_S3_PREFIX", ""),
		StorageCapacityBytes: parseUint64(config.GetEnv("HELMSMAN_STORAGE_CAPACITY_BYTES", "0")),

		// Capabilities (default true)
		CapIngest:     config.GetEnvBool("HELMSMAN_CAP_INGEST", true),
		CapEdge:       config.GetEnvBool("HELMSMAN_CAP_EDGE", true),
		CapStorage:    config.GetEnvBool("HELMSMAN_CAP_STORAGE", true),
		CapProcessing: config.GetEnvBool("HELMSMAN_CAP_PROCESSING", true),

		// Limits (0 = no limit / auto)
		MaxTranscodes: config.GetEnvInt("HELMSMAN_MAX_TRANSCODES", 0),

		// Edge node
		EdgePublicHostname: config.GetEnv("EDGE_PUBLIC_HOSTNAME", ""),
		EnrollmentToken:    config.GetEnv("EDGE_ENROLLMENT_TOKEN", ""),

		// Webhook URL (defaults handled at usage site if empty)
		WebhookURL: config.GetEnv("HELMSMAN_WEBHOOK_URL", ""),

		// gRPC TLS (optional)
		GRPCUseTLS:      config.GetEnvBool("GRPC_USE_TLS", false),
		GRPCTLSCertPath: config.GetEnv("GRPC_TLS_CERT_PATH", ""),
		GRPCTLSKeyPath:  config.GetEnv("GRPC_TLS_KEY_PATH", ""),
	}
}

func parseUint64(s string) uint64 {
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}
