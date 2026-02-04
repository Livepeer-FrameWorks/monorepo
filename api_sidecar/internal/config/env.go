package config

import (
	"os"
	"runtime"
	"strconv"

	"frameworks/pkg/config"

	"golang.org/x/sys/unix"
)

// getSystemMemoryBytes returns total system memory in bytes.
// Uses platform-specific methods: sysinfo on Linux, sysctl on Darwin.
func getSystemMemoryBytes() uint64 {
	// This will be implemented differently per platform via build tags
	return getMemoryBytes()
}

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

	// Cold storage thresholds (S3 credentials are held by Foghorn, not here!)
	// Helmsman receives presigned URLs from Foghorn for secure uploads/downloads
	FreezeThreshold       float64 // Start freezing at this disk usage % (default: 85)
	TargetAfterFreeze     float64 // Target usage after freeze (default: 70)
	DefrostTimeoutSeconds int     // Max wait for sync defrost (default: 30)

	// Capabilities (all default to true)
	CapIngest     bool
	CapEdge       bool
	CapStorage    bool
	CapProcessing bool

	// Limits
	MaxTranscodes int

	// Edge node configuration
	EdgePublicURL   string // Full URL like http://localhost:18090/view
	EnrollmentToken string

	// Webhook URL for MistServer triggers
	WebhookURL string

	// gRPC TLS configuration
	GRPCUseTLS      bool
	GRPCTLSCertPath string
	GRPCTLSKeyPath  string

	// BlockingGraceMs waits for reconnection before failing blocking triggers.
	// Default 2000ms = wait briefly for transient disconnects.
	BlockingGraceMs int

	// RequestedMode is the operational mode this node requests on registration.
	// Foghorn is authoritative and may override this based on DB-persisted state.
	RequestedMode string
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

		// Cold storage thresholds (S3 creds are in Foghorn, not here!)
		FreezeThreshold:       parseFloat64(config.GetEnv("HELMSMAN_FREEZE_THRESHOLD", "0.85")),
		TargetAfterFreeze:     parseFloat64(config.GetEnv("HELMSMAN_TARGET_AFTER_FREEZE", "0.70")),
		DefrostTimeoutSeconds: config.GetEnvInt("HELMSMAN_DEFROST_TIMEOUT_SECONDS", 30),

		// Capabilities (default true)
		CapIngest:     config.GetEnvBool("HELMSMAN_CAP_INGEST", true),
		CapEdge:       config.GetEnvBool("HELMSMAN_CAP_EDGE", true),
		CapStorage:    config.GetEnvBool("HELMSMAN_CAP_STORAGE", true),
		CapProcessing: config.GetEnvBool("HELMSMAN_CAP_PROCESSING", true),

		// Limits (0 = no limit / auto)
		MaxTranscodes: config.GetEnvInt("HELMSMAN_MAX_TRANSCODES", 0),

		// Edge node
		EdgePublicURL:   config.RequireEnv("EDGE_PUBLIC_URL"),
		EnrollmentToken: config.GetEnv("EDGE_ENROLLMENT_TOKEN", ""),

		// Webhook URL (defaults handled at usage site if empty)
		WebhookURL: config.GetEnv("HELMSMAN_WEBHOOK_URL", ""),

		// gRPC TLS (optional)
		GRPCUseTLS:      config.GetEnvBool("GRPC_USE_TLS", false),
		GRPCTLSCertPath: config.GetEnv("GRPC_TLS_CERT_PATH", ""),
		GRPCTLSKeyPath:  config.GetEnv("GRPC_TLS_KEY_PATH", ""),

		BlockingGraceMs: config.GetEnvInt("HELMSMAN_BLOCKING_GRACE_MS", 2000),

		RequestedMode: config.GetEnv("HELMSMAN_OPERATIONAL_MODE", "normal"),
	}
}

func parseUint64(s string) uint64 {
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseUint(s, 10, 64)
	return v
}

func parseFloat64(s string) float64 {
	if s == "" {
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

// GetStoragePath returns the canonical local storage path for Helmsman artifacts.
func GetStoragePath() string {
	storagePath := os.Getenv("HELMSMAN_STORAGE_LOCAL_PATH")
	if storagePath == "" {
		storagePath = "/data/storage"
	}
	return storagePath
}

// HardwareSpecs holds detected hardware information
type HardwareSpecs struct {
	CPUCores int32
	MemoryGB int32
	DiskGB   int32
}

// DetectHardware detects CPU cores, memory, and disk capacity.
// Uses runtime and syscall to get system information.
func DetectHardware(storagePath string) *HardwareSpecs {
	specs := &HardwareSpecs{}

	// CPU cores via runtime
	specs.CPUCores = int32(runtime.NumCPU())

	// Memory via platform-specific implementation (getMemoryBytes in hardware_*.go)
	totalBytes := getSystemMemoryBytes()
	specs.MemoryGB = int32(totalBytes / (1024 * 1024 * 1024))

	// Disk capacity - use storage path if provided, otherwise root
	diskPath := "/"
	if storagePath != "" {
		diskPath = storagePath
	}
	var statfs unix.Statfs_t
	if err := unix.Statfs(diskPath, &statfs); err == nil {
		// Total disk space in bytes, convert to GB
		totalBytes := statfs.Blocks * uint64(statfs.Bsize)
		specs.DiskGB = int32(totalBytes / (1024 * 1024 * 1024))
	}

	return specs
}
