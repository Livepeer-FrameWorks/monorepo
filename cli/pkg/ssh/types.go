package ssh

import (
	"context"
	"time"
)

// ConnectionConfig holds SSH connection parameters
type ConnectionConfig struct {
	Address            string
	Port               int
	User               string
	KeyPath            string
	Timeout            time.Duration
	InsecureSkipVerify bool   // Skip host key verification (DANGEROUS - dev only)
	KnownHostsPath     string // When set, maps to -o UserKnownHostsFile
	// HostName is the logical manifest key (e.g. "central-eu-1"). When non-empty,
	// `ssh -G <HostName>` and `ssh -G frameworks-<HostName>` are consulted and
	// accepted if they resolve to Address. When empty, target is User@Address.
	HostName string
}

// CommandResult holds the result of a command execution
type CommandResult struct {
	Command  string
	ExitCode int
	Stdout   string
	Stderr   string
	Duration time.Duration
	Error    error
}

// UploadOptions holds file upload parameters
type UploadOptions struct {
	LocalPath  string
	RemotePath string
	Mode       uint32 // File permissions (e.g., 0644)
	Owner      string // Optional: chown after upload
	Group      string // Optional: chgrp after upload
}

// Runner executes commands via SSH
type Runner interface {
	// Run executes a command and waits for completion
	Run(ctx context.Context, command string) (*CommandResult, error)

	// RunScript uploads and executes a script
	RunScript(ctx context.Context, script string) (*CommandResult, error)

	// Upload transfers a file via SCP
	Upload(ctx context.Context, opts UploadOptions) error

	// Close releases the connection
	Close() error
}
