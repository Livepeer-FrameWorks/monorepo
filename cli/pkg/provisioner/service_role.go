package provisioner

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"

	"frameworks/cli/pkg/ansiblerun"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// ServiceRoleConfig customizes the generic service-role provisioner for a
// specific service. All fields are optional aside from ServiceName.
type ServiceRoleConfig struct {
	// ServiceName is the manifest key / unit suffix / systemd name piece.
	ServiceName string

	// DefaultPort is used when the manifest does not supply one.
	DefaultPort int

	// HealthPath defaults to "/health" when empty; surfaces in the generated
	// compose healthcheck for docker mode.
	HealthPath string

	// DefaultImage is used when the manifest supplies neither Image nor a
	// gitops release-manifest entry.
	DefaultImage string

	// RuntimePackages are installed before native services start. Debian and
	// Pacman variants cover distro-specific package names for the same sonames.
	RuntimePackages       []string
	DebianRuntimePackages []string
	PacmanRuntimePackages []string
}

// NewServiceRoleProvisioner returns a Provisioner that picks compose_stack
// (docker mode) or go_service (native mode) based on ServiceConfig.Mode and
// builds role vars in Go. Replaces FlexibleProvisioner for generic FrameWorks
// microservices.
func NewServiceRoleProvisioner(cfg ServiceRoleConfig, pool *ssh.Pool) (Provisioner, error) {
	if cfg.ServiceName == "" {
		return nil, fmt.Errorf("ServiceRoleConfig.ServiceName required")
	}
	if cfg.HealthPath == "" {
		cfg.HealthPath = "/health"
	}
	root, err := FindAnsibleRoot()
	if err != nil {
		return nil, fmt.Errorf("%s: locate ansible root: %w", cfg.ServiceName, err)
	}
	exec, err := ansiblerun.NewExecutor()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", cfg.ServiceName, err)
	}
	return &RolePlaybookProvisioner{
		BaseProvisioner:  NewBaseProvisioner(cfg.ServiceName, pool),
		RoleName:         "frameworks.infra.service:" + cfg.ServiceName,
		Builder:          serviceVarsBuilderFor(cfg),
		Detector:         serviceRoleDetect(cfg.ServiceName),
		PlaybookSelector: serviceRolePlaybookSelector,
		AnsibleRoot:      root,
		Executor:         exec,
		Ensurer: &ansiblerun.CollectionEnsurer{
			RequirementsFile: filepath.Join(root, "requirements.yml"),
		},
	}, nil
}

// serviceRolePlaybookSelector picks between compose_stack.yml and
// go_service.yml based on the manifest entry's Mode. An unsupported mode
// surfaces at runtime via the executor's required-playbook check.
func serviceRolePlaybookSelector(config ServiceConfig) string {
	switch config.Mode {
	case "docker":
		return "playbooks/compose_stack.yml"
	case "native":
		return "playbooks/go_service.yml"
	default:
		return ""
	}
}

func serviceVarsBuilderFor(cfg ServiceRoleConfig) RoleVarsBuilder {
	return func(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
		switch config.Mode {
		case "docker":
			return serviceComposeVars(ctx, cfg, host, config, helpers)
		case "native":
			return serviceNativeVars(ctx, cfg, host, config, helpers)
		default:
			return nil, fmt.Errorf("%s: unsupported mode %q (want docker|native)", cfg.ServiceName, config.Mode)
		}
	}
}

func serviceComposeVars(_ context.Context, cfg ServiceRoleConfig, _ inventory.Host, config ServiceConfig, _ RoleBuildHelpers) (map[string]any, error) {
	port := config.Port
	if port == 0 {
		port = cfg.DefaultPort
	}
	image, err := resolveGenericImage(cfg, config)
	if err != nil {
		return nil, err
	}
	envMap := buildServiceEnvMap(config)
	envAny := make(map[string]any, len(envMap))
	for k, v := range envMap {
		envAny[k] = v
	}
	return map[string]any{
		"compose_stack_name":        cfg.ServiceName,
		"compose_stack_project_dir": "/opt/frameworks/" + cfg.ServiceName,
		"compose_stack_service": map[string]any{
			"image":       image,
			"port":        port,
			"health_path": cfg.HealthPath,
		},
		"compose_stack_env": envAny,
	}, nil
}

func serviceNativeVars(ctx context.Context, cfg ServiceRoleConfig, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
	url, checksum, binaryName, err := resolveGenericBinary(ctx, cfg.ServiceName, host, config, helpers)
	if err != nil {
		return nil, err
	}
	port := config.Port
	if port == 0 {
		port = cfg.DefaultPort
	}
	envMap := buildServiceEnvMap(config)
	envAny := make(map[string]any, len(envMap))
	for k, v := range envMap {
		envAny[k] = v
	}
	vars := map[string]any{
		"go_service_name":                    cfg.ServiceName,
		"go_service_artifact_url":            url,
		"go_service_artifact_checksum":       checksum,
		"go_service_version":                 firstNonEmpty(config.Version, metaString(config.Metadata, "version")),
		"go_service_port":                    port,
		"go_service_env":                     envAny,
		"go_service_defer_start":             config.DeferStart,
		"go_service_binary_name":             binaryName,
		"go_service_runtime_packages":        cfg.RuntimePackages,
		"go_service_debian_runtime_packages": cfg.DebianRuntimePackages,
		"go_service_pacman_runtime_packages": cfg.PacmanRuntimePackages,
	}
	if ca := metaString(config.Metadata, "internal_ca_bundle_pem"); ca != "" {
		vars["go_service_internal_ca_bundle_pem"] = ca
	}
	if cert := metaString(config.Metadata, "internal_tls_cert_pem"); cert != "" {
		vars["go_service_internal_tls_cert_pem"] = cert
	}
	if key := metaString(config.Metadata, "internal_tls_key_pem"); key != "" {
		vars["go_service_internal_tls_key_pem"] = key
	}
	return vars, nil
}

func buildServiceEnvMap(config ServiceConfig) map[string]string {
	out := map[string]string{}
	maps.Copy(out, config.EnvVars)
	if clusterID, ok := config.Metadata["cluster_id"].(string); ok && clusterID != "" {
		out["CLUSTER_ID"] = clusterID
	}
	if nodeID, ok := config.Metadata["node_id"].(string); ok && nodeID != "" {
		out["NODE_ID"] = nodeID
	}
	return out
}

func resolveGenericImage(cfg ServiceRoleConfig, config ServiceConfig) (string, error) {
	if config.Image != "" {
		return config.Image, nil
	}
	if cfg.DefaultImage != "" {
		return cfg.DefaultImage, nil
	}
	image, err := imageFromReleaseManifest(cfg.ServiceName, config.Version, config.Metadata)
	if err != nil {
		return "", fmt.Errorf("resolve %s image: %w", cfg.ServiceName, err)
	}
	return image, nil
}

func resolveGenericBinary(ctx context.Context, serviceName string, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (string, string, string, error) {
	if config.BinaryURL != "" {
		return config.BinaryURL, "", serviceName, nil
	}
	channel, version := gitops.ResolveVersion(config.Version)
	manifest, err := fetchGitopsManifest(channel, version, config.Metadata)
	if err != nil {
		return "", "", "", err
	}
	remoteOS, remoteArch, err := helpers.DetectRemoteOS(ctx, host)
	if err != nil {
		return "", "", "", fmt.Errorf("detect arch: %w", err)
	}
	svc, err := manifest.GetServiceInfo(serviceName)
	if err == nil {
		bin, binErr := svc.GetBinary(remoteOS, remoteArch)
		if binErr != nil {
			return "", "", "", binErr
		}
		return bin.URL, bin.Checksum, serviceName, nil
	}
	if bin, depName := binaryFromExternalDependency(serviceName, remoteOS, remoteArch, manifest); bin != nil {
		return bin.URL, bin.Checksum, "livepeer", nil
	} else if depName != "" {
		return "", "", "", fmt.Errorf("external dependency %s has no %s-%s binary for %s", depName, remoteOS, remoteArch, serviceName)
	}
	return "", "", "", err
}

func serviceRoleDetect(_ string) RoleDetector {
	return func(ctx context.Context, host inventory.Host, helpers RoleBuildHelpers) (*detect.ServiceState, error) {
		return &detect.ServiceState{Exists: false, Running: false}, nil
	}
}
