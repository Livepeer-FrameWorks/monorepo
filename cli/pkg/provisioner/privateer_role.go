package provisioner

import (
	"context"
	"strings"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

func privateerRoleVars(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
	_, arch, err := helpers.DetectRemoteOS(ctx, host)
	if err != nil {
		return nil, err
	}
	archKey := "linux-" + arch
	channel := platformChannelFromMetadata(config.Metadata)
	art, err := helpers.ResolveArtifact("privateer", archKey, channel, config.Metadata)
	if err != nil {
		return nil, err
	}

	vars := map[string]any{
		"privateer_artifact_url":      art.URL,
		"privateer_artifact_checksum": art.Checksum,
		"privateer_version":           firstNonEmpty(config.Version, art.Version),
	}
	if p := config.Port; p > 0 {
		vars["privateer_port"] = p
	}
	if env, ok := config.Metadata["env"].(map[string]string); ok && len(env) > 0 {
		envAny := make(map[string]any, len(env))
		for k, v := range env {
			envAny[k] = v
		}
		vars["privateer_env"] = envAny
	}
	return vars, nil
}

func privateerRoleDetect(ctx context.Context, host inventory.Host, helpers RoleBuildHelpers) (*detect.ServiceState, error) {
	if host.ExternalIP == "127.0.0.1" || host.ExternalIP == "localhost" {
		return &detect.ServiceState{Exists: false, Running: false}, nil
	}
	runner, err := helpers.SSHPool.Get(&ssh.ConnectionConfig{
		Address: host.ExternalIP, Port: 22, User: host.User, HostName: host.Name, Timeout: 10 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	result, _ := runner.Run(ctx, "systemctl is-active frameworks-privateer 2>/dev/null | grep -qx active && echo RUNNING || echo NOT_RUNNING")
	running := result != nil && strings.Contains(result.Stdout, "RUNNING") && !strings.Contains(result.Stdout, "NOT_RUNNING")
	bin, _ := runner.Run(ctx, "test -x /opt/privateer/privateer && echo EXISTS")
	exists := bin != nil && strings.Contains(bin.Stdout, "EXISTS")
	return &detect.ServiceState{Exists: exists, Running: running}, nil
}
