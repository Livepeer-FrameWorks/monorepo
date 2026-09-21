package config

import (
	"runtime"

	"frameworks/api_sidecar/internal/appconfig"

	"golang.org/x/sys/unix"
)

// getSystemMemoryBytes returns total system memory in bytes.
// Uses platform-specific methods: sysinfo on Linux, sysctl on Darwin.
func getSystemMemoryBytes() uint64 {
	// This will be implemented differently per platform via build tags
	return getMemoryBytes()
}

// HelmsmanConfig is the startup configuration snapshot handed to the control
// client. It is built once from the typed appconfig.Helmsman; values that must
// follow a SIGHUP reload are read through appconfig.Runtime instead.
type HelmsmanConfig struct {
	// Required - service identity
	NodeID             string
	FoghornControlAddr string

	// MistServer connection
	MistServerURL   string
	MistAPIUsername string
	MistAPIPassword string

	// Storage configuration
	StateDir             string
	StorageLocalPath     string
	StorageS3Bucket      string
	StorageS3Prefix      string
	StorageCapacityBytes uint64

	// Cold storage thresholds (S3 credentials are held by Foghorn, not here!)
	// Helmsman receives presigned URLs from Foghorn for secure uploads/downloads
	FreezeThreshold   float64 // Start freezing at this disk usage fraction (default: 0.85)
	TargetAfterFreeze float64 // Target usage after freeze (default: 0.70)

	// Capabilities (all default to true)
	CapIngest     bool
	CapEdge       bool
	CapStorage    bool
	CapProcessing bool

	// Limits
	MaxTranscodes int

	// Edge node configuration
	EdgePublicURL       string // Full URL like http://localhost:18090/view
	EnrollmentToken     string
	RotateNodeIdentity  bool
	EnrollmentTokenFile string
	RuntimeEnvFile      string

	// Webhook URL for MistServer triggers
	WebhookURL string

	// gRPC TLS / trust configuration
	GRPCAllowInsecure bool
	GRPCTLSCertPath   string
	GRPCTLSKeyPath    string
	GRPCTLSCAPath     string
	GRPCTLSServerName string

	// BlockingGraceMs waits for reconnection before failing blocking triggers.
	// Default 2000ms = wait briefly for transient disconnects.
	BlockingGraceMs int

	// RequestedMode is the operational mode this node requests on registration.
	// Foghorn is authoritative and may override this based on DB-persisted state.
	RequestedMode string

	// RelayTrustedCIDR is a comma-separated CIDR list whose RemoteAddr
	// bypasses the relay authorize gate like loopback (still requiring no
	// proxy-forward markers). Only needed when Mist dials Helmsman over a
	// non-loopback address. Empty on native hosts and in the edge container,
	// dev compose included, where Mist reaches Helmsman on 127.0.0.1.
	// NEVER set to a range that covers peer nodes — peer reads authorize
	// via Foghorn.
	RelayTrustedCIDR string
}

// NewHelmsmanConfig builds the control client's startup snapshot from the
// typed Helmsman configuration. Malformed numeric and boolean values resolve
// to the same fallbacks the environment readers used before typed
// configuration.
func NewHelmsmanConfig(cfg *appconfig.Helmsman) *HelmsmanConfig {
	capIngest, capEdge, capStorage, capProcessing := cfg.Capabilities()
	freezeThreshold, targetAfterFreeze := cfg.StorageThresholds()
	return &HelmsmanConfig{
		NodeID:             cfg.NodeID,
		FoghornControlAddr: cfg.FoghornControlAddr,

		MistServerURL:   cfg.MistServerURL,
		MistAPIUsername: cfg.MistAPIUsername,
		MistAPIPassword: cfg.MistAPIPassword,

		StateDir:             cfg.StateDir,
		StorageLocalPath:     cfg.StorageLocalPath,
		StorageS3Bucket:      cfg.StorageS3Bucket,
		StorageS3Prefix:      cfg.StorageS3Prefix,
		StorageCapacityBytes: cfg.StorageCapacity(),

		FreezeThreshold:   freezeThreshold,
		TargetAfterFreeze: targetAfterFreeze,

		CapIngest:     capIngest,
		CapEdge:       capEdge,
		CapStorage:    capStorage,
		CapProcessing: capProcessing,

		MaxTranscodes: cfg.MaxTranscodeSlots(),

		EdgePublicURL:       cfg.EdgePublicURL,
		EnrollmentToken:     cfg.EnrollmentToken,
		RotateNodeIdentity:  cfg.RotateNodeIdentityRequested(),
		EnrollmentTokenFile: cfg.EnrollmentTokenFile,
		RuntimeEnvFile:      cfg.RuntimeEnvFile,

		WebhookURL: cfg.MistWebhookBaseURL,

		RelayTrustedCIDR: cfg.RelayTrustedCIDR,

		GRPCAllowInsecure: cfg.GRPCInsecureAllowed(),
		GRPCTLSCertPath:   cfg.GRPCTLSCertPath,
		GRPCTLSKeyPath:    cfg.GRPCTLSKeyPath,
		GRPCTLSCAPath:     cfg.GRPCTLSCAPath,
		GRPCTLSServerName: cfg.FoghornGRPCTLSServerName,

		BlockingGraceMs: cfg.BlockingGrace(),

		RequestedMode: cfg.RequestedOperationalMode,
	}
}

// GetStoragePath returns the canonical local storage path for Helmsman artifacts.
func GetStoragePath() string {
	return appconfig.Runtime().StoragePathOrDefault()
}

// GetStorageCapacityBytes returns HELMSMAN_STORAGE_CAPACITY_BYTES, or 0 when it
// is unset or not an unsigned integer.
func GetStorageCapacityBytes() uint64 {
	return appconfig.Runtime().StorageCapacity()
}

// ConfiguredBandwidthLimitBytesPerSec returns an operator-pinned node bandwidth
// limit. The canonical env uses bytes/sec because Foghorn's NodeLifecycleUpdate
// stores bw_limit in bytes/sec; HELMSMAN_BW_LIMIT_MBPS is accepted for operator
// convenience.
func ConfiguredBandwidthLimitBytesPerSec() uint64 {
	return appconfig.Runtime().BandwidthLimitBytesPerSecond()
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
