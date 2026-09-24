package provisioner

import (
	"context"
	"fmt"
	"strings"

	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/system"
)

const edgeONNXProfileAuto = "auto"

// edgeNVIDIARuntimeProbe exits 0 when the Docker daemon lists an NVIDIA runtime.
var edgeNVIDIARuntimeProbe = system.DockerCommand("info --format '{{json .Runtimes}}'") + " 2>/dev/null | grep -qi nvidia"

func resolvedEdgeONNXProfile(profile, mode, osName, arch string) string {
	profile = strings.ToLower(strings.TrimSpace(profile))
	if profile != "" && profile != edgeONNXProfileAuto {
		return profile
	}
	if mode == "native" && osName == "darwin" && arch == "arm64" {
		return "coreml"
	}
	return "cpu"
}

func selectEdgeONNXProfile(requested, mode, osName, arch string, hasNVIDIA, hasIntel bool) (string, error) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested == "" {
		requested = edgeONNXProfileAuto
	}
	valid := map[string]bool{
		edgeONNXProfileAuto: true,
		"cpu":               true,
		"coreml":            true,
		"cuda":              true,
		"tensorrt":          true,
		"openvino":          true,
	}
	if !valid[requested] {
		return "", fmt.Errorf("edge: invalid ONNX profile %q (valid: auto, cpu, coreml, cuda, tensorrt, openvino)", requested)
	}

	if requested == edgeONNXProfileAuto {
		switch {
		case mode == "native" && osName == "darwin" && arch == "arm64":
			return "coreml", nil
		case osName == "linux" && arch == "amd64" && hasNVIDIA:
			return "cuda", nil
		case osName == "linux" && arch == "amd64" && hasIntel:
			return "openvino", nil
		default:
			return "cpu", nil
		}
	}

	switch requested {
	case "cpu":
		if mode == "native" && osName == "darwin" {
			return "", fmt.Errorf("edge: CPU-only MistServer is not published for native macOS; use coreml or auto")
		}
	case "coreml":
		if mode != "native" || osName != "darwin" || arch != "arm64" {
			return "", fmt.Errorf("edge: CoreML requires native darwin/arm64 deployment")
		}
	case "cuda", "tensorrt":
		if osName != "linux" || arch != "amd64" {
			return "", fmt.Errorf("edge: %s requires linux/amd64", requested)
		}
		if !hasNVIDIA {
			return "", fmt.Errorf("edge: %s requested but a usable NVIDIA device/runtime was not detected", requested)
		}
	case "openvino":
		if osName != "linux" || arch != "amd64" {
			return "", fmt.Errorf("edge: OpenVINO requires linux/amd64")
		}
		if !hasIntel {
			return "", fmt.Errorf("edge: OpenVINO requested but no Intel CPU was detected")
		}
	}
	return requested, nil
}

func (e *EdgeProvisioner) resolveONNXProfile(ctx context.Context, host inventory.Host, mode, osName, arch, requested string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(requested))
	needsProbe := osName == "linux" && arch == "amd64" &&
		(normalized == "" || normalized == edgeONNXProfileAuto || normalized == "cuda" || normalized == "tensorrt" || normalized == "openvino")
	if !needsProbe {
		return selectEdgeONNXProfile(normalized, mode, osName, arch, false, false)
	}

	hasNVIDIA := false
	if result, err := e.RunCommand(ctx, host, "command -v nvidia-smi >/dev/null 2>&1 && nvidia-smi -L"); err == nil && result.ExitCode == 0 && strings.TrimSpace(result.Stdout) != "" {
		hasNVIDIA = true
	}
	if hasNVIDIA && mode == "container" {
		if result, err := e.RunCommand(ctx, host, edgeNVIDIARuntimeProbe); err != nil || result.ExitCode != 0 {
			hasNVIDIA = false
		}
	}
	hasIntel := false
	if result, err := e.RunCommand(ctx, host, "grep -qi GenuineIntel /proc/cpuinfo"); err == nil && result.ExitCode == 0 {
		hasIntel = true
	}
	selected, err := selectEdgeONNXProfile(normalized, mode, osName, arch, hasNVIDIA, hasIntel)
	if err != nil {
		return "", err
	}
	if mode == "native" && osName == "linux" && (selected == "cuda" || selected == "tensorrt" || selected == "openvino") {
		compatible := false
		if result, probeErr := e.RunCommand(ctx, host, "test \"$(. /etc/os-release && printf '%s:%s' \"$ID\" \"$VERSION_ID\")\" = ubuntu:24.04"); probeErr == nil && result.ExitCode == 0 {
			compatible = true
		}
		if !compatible {
			if normalized == "" || normalized == edgeONNXProfileAuto {
				return "cpu", nil
			}
			return "", fmt.Errorf("edge: native %s bundle requires Ubuntu 24.04; use cpu or container mode on this host", selected)
		}
	}
	return selected, nil
}
