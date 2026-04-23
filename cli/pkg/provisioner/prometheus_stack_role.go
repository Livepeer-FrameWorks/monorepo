package provisioner

import (
	"context"
	"strings"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// prometheusStackRoleVars maps observability.* manifest entries into the
// prometheus_stack role vars. The CLI passes one component per call
// (prometheus, victoriametrics, vmagent, vmauth) and the role dispatches on
// prometheus_stack_components.
func prometheusStackRoleVars(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
	component := metaString(config.Metadata, "component")
	if component == "" {
		component = metaString(config.Metadata, "service_name")
	}

	vars := map[string]any{
		"prometheus_stack_components": []string{component},
	}

	_, arch, err := helpers.DetectRemoteOS(ctx, host)
	if err == nil {
		archKey := "linux-" + arch
		channel := platformChannelFromMetadata(config.Metadata)
		// For the VM-flavor components, resolve pinned artifacts.
		switch component {
		case "victoriametrics":
			if art, err := helpers.ResolveArtifact("victoriametrics", archKey, channel, config.Metadata); err == nil {
				vars["victoriametrics_artifact_url"] = art.URL
				vars["victoriametrics_artifact_checksum"] = art.Checksum
				vars["victoriametrics_version"] = firstNonEmpty(config.Version, art.Version)
			}
		case "vmagent":
			if art, err := helpers.ResolveArtifact("vmagent", archKey, channel, config.Metadata); err == nil {
				vars["vmagent_artifact_url"] = art.URL
				vars["vmagent_artifact_checksum"] = art.Checksum
				vars["vmagent_version"] = firstNonEmpty(config.Version, art.Version)
			}
			if rw, ok := config.Metadata["remote_write_url"].(string); ok {
				vars["vmagent_remote_write_url"] = rw
			}
		case "vmauth":
			if art, err := helpers.ResolveArtifact("vmauth", archKey, channel, config.Metadata); err == nil {
				vars["vmauth_artifact_url"] = art.URL
				vars["vmauth_artifact_checksum"] = art.Checksum
				vars["vmauth_version"] = firstNonEmpty(config.Version, art.Version)
			}
		case "prometheus":
			if v := firstNonEmpty(config.Version, metaString(config.Metadata, "version")); v != "" {
				vars["prometheus_version"] = v
			}
		}
	}

	if port := config.Port; port > 0 {
		switch component {
		case "prometheus":
			vars["prometheus_port"] = port
		case "victoriametrics":
			vars["victoriametrics_port"] = port
		case "vmagent":
			vars["vmagent_port"] = port
		case "vmauth":
			vars["vmauth_port"] = port
		}
	}
	return vars, nil
}

func prometheusStackRoleDetect(ctx context.Context, host inventory.Host, helpers RoleBuildHelpers) (*detect.ServiceState, error) {
	if host.ExternalIP == "127.0.0.1" || host.ExternalIP == "localhost" {
		return &detect.ServiceState{Exists: false, Running: false}, nil
	}
	runner, err := helpers.SSHPool.Get(&ssh.ConnectionConfig{
		Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 10 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	result, runErr := runner.Run(ctx, "systemctl is-active prometheus victoriametrics vmagent vmauth node_exporter 2>/dev/null | grep -qx active && echo RUNNING || echo NOT_RUNNING")
	running := runErr == nil && result != nil && strings.Contains(result.Stdout, "RUNNING") && !strings.Contains(result.Stdout, "NOT_RUNNING")
	return &detect.ServiceState{Exists: running, Running: running}, nil
}
