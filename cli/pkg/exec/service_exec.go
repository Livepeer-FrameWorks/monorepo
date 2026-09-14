// Package exec builds shell commands that invoke installed service binaries
// on remote hosts, dispatching on detected deployment mode (docker | native).
// It is consumed by cluster-side commands that need to talk to a service's
// own CLI surface (e.g. data-migrations) over SSH.
//
// Centralizing the docker-exec / native-binary branching here keeps every
// caller from inventing its own command-assembly logic.
package exec

import (
	"fmt"
	"path"
	"strings"
)

// Mode is the detected deployment mode of a service on its host. Any value
// other than ModeDocker is treated as a native binary on disk.
type Mode string

const (
	ModeDocker Mode = "docker"
	ModeNative Mode = "native"
)

// Spec describes a single remote invocation: which deployment mode the
// service runs in, the container or binary name, and the path to its
// binary on disk (native mode) or inside the container (docker mode).
type Spec struct {
	Mode Mode

	// ContainerName is the docker container name (docker mode). When empty,
	// falls back to BinaryName.
	ContainerName string

	// BinaryName is the service's installed binary name (e.g. "purser").
	// In docker mode, this is the binary inside the container; in native
	// mode, used to derive InstallPath if not set.
	BinaryName string

	// InstallPath is the absolute path to the native binary on the host.
	// When empty, defaults to the go_service role's canonical install path.
	InstallPath string

	// EnvFile, WorkingDirectory, and User reproduce the native systemd
	// service's execution context. They are optional for raw native commands.
	EnvFile          string
	WorkingDirectory string
	User             string
}

// SpecFromDetection converts service-detector output into an invocation spec.
// The detector's exact container and binary paths take precedence over the
// deployment conventions used when older inventory lacks that metadata.
func SpecFromDetection(mode string, metadata map[string]string, binaryName string) Spec {
	spec := Spec{BinaryName: binaryName}
	if Mode(mode) == ModeDocker {
		spec.Mode = ModeDocker
		spec.ContainerName = metadata["container_name"]
		return spec
	}
	spec.Mode = ModeNative
	spec.InstallPath = metadata["binary_path"]
	if spec.InstallPath == "" {
		spec.InstallPath = path.Join("/opt/frameworks", binaryName, binaryName)
	}
	spec.EnvFile = metadata["environment_file"]
	if spec.EnvFile == "" {
		spec.EnvFile = path.Join("/etc/frameworks", binaryName+".env")
	}
	spec.WorkingDirectory = metadata["working_directory"]
	if spec.WorkingDirectory == "" {
		spec.WorkingDirectory = path.Dir(spec.InstallPath)
	}
	spec.User = metadata["service_user"]
	if spec.User == "" {
		spec.User = "frameworks"
	}
	return spec
}

// Command returns the remote shell command that invokes the service binary
// with args. Args are quoted with single quotes; embedded single quotes are
// safely escaped via the standard shell idiom '\”.
func Command(s Spec, args []string) (string, error) {
	if s.BinaryName == "" {
		return "", fmt.Errorf("exec.Command: empty BinaryName")
	}

	switch s.Mode {
	case ModeDocker:
		container := s.ContainerName
		if container == "" {
			container = s.BinaryName
		}
		parts := []string{"docker", "exec", quote(container), quote(s.BinaryName)}
		for _, a := range args {
			parts = append(parts, quote(a))
		}
		return strings.Join(parts, " "), nil
	case ModeNative, "":
		bin := s.InstallPath
		if bin == "" {
			bin = path.Join("/opt/frameworks", s.BinaryName, s.BinaryName)
		}
		if s.EnvFile != "" {
			workingDirectory := s.WorkingDirectory
			if workingDirectory == "" {
				workingDirectory = path.Dir(bin)
			}
			parts := []string{}
			if s.User != "" {
				parts = append(parts, "sudo", "-u", quote(s.User), "--")
			}
			parts = append(parts,
				"/bin/bash", "-eu", "-c",
				quote(`set -a; . "$1"; set +a; cd "$2"; shift 2; exec "$@"`),
				"--", quote(s.EnvFile), quote(workingDirectory), quote(bin),
			)
			for _, a := range args {
				parts = append(parts, quote(a))
			}
			return strings.Join(parts, " "), nil
		}
		parts := []string{quote(bin)}
		for _, a := range args {
			parts = append(parts, quote(a))
		}
		return strings.Join(parts, " "), nil
	default:
		return "", fmt.Errorf("exec.Command: unsupported mode %q", s.Mode)
	}
}

// ShellQuote single-quotes s for safe interpolation into remote shell
// commands that are not service invocations.
func ShellQuote(s string) string {
	return quote(s)
}

// quote single-quotes s for POSIX-shell-safe argv. Empty string becomes ”.
func quote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"\\$`") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
