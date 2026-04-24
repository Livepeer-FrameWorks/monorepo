package provisioner

import (
	"context"
	"maps"
	"strconv"
	"strings"
	"time"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
	infra "frameworks/pkg/models"
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

	env := map[string]string{}
	maps.Copy(env, config.EnvVars)
	if metaEnv, ok := config.Metadata["env"].(map[string]string); ok {
		maps.Copy(env, metaEnv)
	}
	if host.Name != "" && env["MESH_NODE_NAME"] == "" {
		env["MESH_NODE_NAME"] = host.Name
	}
	if env["MESH_NODE_TYPE"] == "" {
		env["MESH_NODE_TYPE"] = privateerNodeType(host)
	}
	if host.ExternalIP != "" && env["MESH_EXTERNAL_IP"] == "" {
		env["MESH_EXTERNAL_IP"] = host.ExternalIP
	}
	if env["PRIVATEER_DATA_DIR"] == "" {
		env["PRIVATEER_DATA_DIR"] = "/var/lib/privateer"
	}
	if ip, ok := config.Metadata["wireguard_ip"].(string); ok && ip != "" && env["MESH_WIREGUARD_IP"] == "" {
		env["MESH_WIREGUARD_IP"] = ip
	}
	if port, ok := config.Metadata["wireguard_port"].(int); ok && port > 0 && env["MESH_LISTEN_PORT"] == "" {
		env["MESH_LISTEN_PORT"] = strconv.Itoa(port)
	}
	if env["PRIVATEER_STATIC_PEERS_FILE"] == "" {
		env["PRIVATEER_STATIC_PEERS_FILE"] = "/etc/privateer/static-peers.json"
	}
	if env["MESH_PRIVATE_KEY_FILE"] == "" {
		if priv, ok := config.Metadata["wireguard_private_key"].(string); ok && priv != "" {
			// SOPS-managed seed path: Ansible will render the key into the
			// default location.
			env["MESH_PRIVATE_KEY_FILE"] = "/etc/privateer/wg.key"
		} else if keyFile, ok := config.Metadata["wireguard_private_key_file"].(string); ok && keyFile != "" {
			// Adopted-local path: no SOPS key, inventory names the on-disk
			// file. Ansible's preserve-key branch asserts it exists and
			// leaves it untouched.
			env["MESH_PRIVATE_KEY_FILE"] = keyFile
		}
	}
	if services, ok := config.Metadata["expected_internal_grpc_services"].([]string); ok && len(services) > 0 && env["EXPECTED_INTERNAL_GRPC_SERVICES"] == "" {
		env["EXPECTED_INTERNAL_GRPC_SERVICES"] = strings.Join(services, ",")
	}
	if len(env) > 0 {
		envAny := make(map[string]any, len(env))
		for k, v := range env {
			envAny[k] = v
		}
		vars["privateer_env"] = envAny
	}

	if peers, ok := config.Metadata["static_peers"].([]map[string]any); ok {
		vars["privateer_static_peers"] = peers
	}
	if dns, ok := config.Metadata["static_dns"].(map[string][]string); ok {
		vars["privateer_static_dns"] = dns
	}
	if ip, ok := config.Metadata["wireguard_ip"].(string); ok && ip != "" {
		vars["privateer_wireguard_ip"] = ip
	}
	if priv, ok := config.Metadata["wireguard_private_key"].(string); ok && priv != "" {
		vars["privateer_wireguard_private_key"] = priv
	}
	if port, ok := config.Metadata["wireguard_port"].(int); ok && port > 0 {
		vars["privateer_wireguard_port"] = port
	}
	// Adopted-local nodes surface a boolean that gates the preserve-key
	// branch in the Ansible role. Only emit it when explicitly set in the
	// inventory, so older (SOPS-managed-only) clusters render identically.
	if managed, ok := config.Metadata["wireguard_private_key_managed"].(bool); ok {
		vars["privateer_wireguard_private_key_managed"] = managed
	}
	return vars, nil
}

func privateerNodeType(host inventory.Host) string {
	for _, role := range host.Roles {
		if role == infra.NodeTypeEdge {
			return infra.NodeTypeEdge
		}
	}
	return infra.NodeTypeCore
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
	result, runErr := runner.Run(ctx, "systemctl is-active frameworks-privateer 2>/dev/null | grep -qx active && echo RUNNING || echo NOT_RUNNING")
	running := runErr == nil && result != nil && strings.Contains(result.Stdout, "RUNNING") && !strings.Contains(result.Stdout, "NOT_RUNNING")
	bin, binErr := runner.Run(ctx, "test -x /opt/privateer/privateer && echo EXISTS")
	exists := binErr == nil && bin != nil && strings.Contains(bin.Stdout, "EXISTS")
	return &detect.ServiceState{Exists: exists, Running: running}, nil
}
