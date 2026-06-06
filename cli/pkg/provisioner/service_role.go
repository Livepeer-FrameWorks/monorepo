package provisioner

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"frameworks/cli/pkg/ansiblerun"
	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

// ServiceRoleConfig customizes the generic service-role provisioner for a
// specific service. All fields are optional aside from ServiceName.
type ServiceRoleConfig struct {
	// ServiceName is the manifest key / unit suffix / systemd name piece.
	ServiceName string

	// DefaultPort is used when the manifest does not supply one.
	DefaultPort int

	// HealthPath defaults to "/health" when empty; surfaces in the generated
	// compose HTTP validation for docker mode.
	HealthPath string

	// ContainerPort is the port the container listens on in docker mode.
	// DefaultPort remains the host-facing port published by Docker.
	ContainerPort int

	// DefaultImage is used when the manifest supplies neither Image nor a
	// gitops release-manifest entry.
	DefaultImage string

	// RuntimePackages are installed before native services start. Debian and
	// Pacman variants cover distro-specific package names for the same sonames.
	RuntimePackages       []string
	DebianRuntimePackages []string
	PacmanRuntimePackages []string

	// StateDirs are writable data directories required by native services.
	StateDirs []string

	// Args are appended to ExecStart for native services.
	Args []string

	// DataMigrations means the service binary dispatches pkg/datamigrate's
	// data-migrations command surface. The provisioner installs the host marker
	// the cluster CLI checks before invoking that command.
	DataMigrations bool
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
		Fingerprinter:    serviceRoleFingerprint(cfg),
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
		if metaBool(config.Metadata, "_cleanup_only", false) {
			return serviceCleanupVars(cfg, config)
		}
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

func serviceCleanupVars(cfg ServiceRoleConfig, config ServiceConfig) (map[string]any, error) {
	switch config.Mode {
	case "docker":
		return map[string]any{
			"compose_stack_name":        cfg.ServiceName,
			"compose_stack_project_dir": "/opt/frameworks/" + cfg.ServiceName,
		}, nil
	case "native":
		return map[string]any{
			"go_service_name": cfg.ServiceName,
		}, nil
	default:
		return nil, fmt.Errorf("%s: unsupported mode %q (want docker|native)", cfg.ServiceName, config.Mode)
	}
}

func serviceComposeVars(_ context.Context, cfg ServiceRoleConfig, _ inventory.Host, config ServiceConfig, _ RoleBuildHelpers) (map[string]any, error) {
	port := config.Port
	if port == 0 {
		port = cfg.DefaultPort
	}
	containerPort := metaIntOr(config.Metadata, "container_port", cfg.ContainerPort)
	if containerPort == 0 {
		containerPort = port
	}
	healthPath := firstNonEmpty(metaString(config.Metadata, "health_path"), cfg.HealthPath)
	image, err := resolveGenericImage(cfg, config)
	if err != nil {
		return nil, err
	}
	envMap := buildServiceEnvMap(config)
	composeFiles := map[string]string{}
	composeVolumes := []string{}
	composeStateDirs := []map[string]string{}
	composeExtraHosts := []string{}
	if cfg.ServiceName == "skipper" {
		var err error
		composeFiles, composeVolumes, err = skipperComposeSourceFiles(envMap)
		if err != nil {
			return nil, err
		}
	}
	if cfg.ServiceName == "metabase" {
		applyMetabaseComposeDefaults(envMap)
		composeVolumes = append(composeVolumes, "/var/lib/frameworks/metabase:/metabase-data")
		composeStateDirs = append(composeStateDirs, map[string]string{
			"path":  "/var/lib/frameworks/metabase",
			"owner": "2000",
			"group": "2000",
			"mode":  "0750",
		})
		if metabaseUsesDockerHostGateway(envMap) {
			composeExtraHosts = append(composeExtraHosts, "host.docker.internal:host-gateway")
		}
	}
	if cfg.ServiceName == "grafana" {
		composeVolumes = append(composeVolumes, "/var/lib/frameworks/grafana:/var/lib/grafana")
		composeStateDirs = append(composeStateDirs, map[string]string{
			"path":  "/var/lib/frameworks/grafana",
			"owner": "472",
			"group": "0",
			"mode":  "0750",
		})
	}
	envAny := make(map[string]any, len(envMap))
	for k, v := range envMap {
		envAny[k] = v
	}
	return map[string]any{
		"compose_stack_name":        cfg.ServiceName,
		"compose_stack_project_dir": "/opt/frameworks/" + cfg.ServiceName,
		"compose_stack_wait":        false,
		"compose_stack_registry_auth": composeRegistryAuthFromEnv(
			config.EnvVars,
			image,
		),
		"compose_stack_require_registry_auth": composeRegistryAuthRequired(image),
		"compose_stack_files":                 composeFiles,
		"compose_stack_service": map[string]any{
			"image":          image,
			"port":           port,
			"container_port": containerPort,
			"health_path":    healthPath,
			"volumes":        composeVolumes,
			"extra_hosts":    composeExtraHosts,
		},
		"compose_stack_env":                    envAny,
		"compose_stack_state_dirs":             composeStateDirs,
		"compose_stack_data_migrations_marker": dataMigrationsMarker(cfg, config),
	}, nil
}

func applyMetabaseComposeDefaults(env map[string]string) {
	if env["MB_DB_TYPE"] == "" {
		env["MB_DB_TYPE"] = "postgres"
	}
	if env["MB_DB_HOST"] == "" {
		env["MB_DB_HOST"] = rewriteLoopbackForDockerHost(firstNonEmpty(env["DATABASE_HOST"], "127.0.0.1"))
	}
	if env["MB_DB_PORT"] == "" {
		env["MB_DB_PORT"] = firstNonEmpty(env["DATABASE_PORT"], "5432")
	}
	if env["MB_DB_DBNAME"] == "" {
		env["MB_DB_DBNAME"] = firstNonEmpty(env["DATABASE_NAME"], "metabase")
	}
	if env["MB_DB_USER"] == "" {
		env["MB_DB_USER"] = firstNonEmpty(env["DATABASE_USER"], "metabase")
	}
	if env["MB_DB_PASS"] == "" {
		env["MB_DB_PASS"] = env["DATABASE_PASSWORD"]
	}
}

func metabaseUsesDockerHostGateway(env map[string]string) bool {
	return env["MB_DB_HOST"] == "host.docker.internal" || strings.HasPrefix(env["MB_DB_HOST"], "host.docker.internal:")
}

func composeRegistryAuthFromEnv(env map[string]string, image string) map[string]any {
	if env == nil {
		env = map[string]string{}
	}

	registry := firstNonEmpty(
		registryEnv(env, "DOCKER_REGISTRY"),
		registryEnv(env, "CONTAINER_REGISTRY"),
		registryEnv(env, "REGISTRY_URL"),
		registryEnv(env, "REGISTRY_HOST"),
	)
	if registry == "" && strings.HasPrefix(image, "ghcr.io/") {
		registry = "ghcr.io"
	}

	username := firstNonEmpty(
		registryEnv(env, "DOCKER_USERNAME"),
		registryEnv(env, "DOCKER_USER"),
		registryEnv(env, "REGISTRY_USERNAME"),
		registryEnv(env, "REGISTRY_USER"),
		registryEnv(env, "GHCR_USERNAME"),
		registryEnv(env, "GHCR_USER"),
		registryEnv(env, "GHCR_OWNER"),
		registryEnv(env, "GITHUB_ACTOR"),
		registryEnv(env, "GITHUB_USERNAME"),
		registryEnv(env, "GITHUB_USER"),
	)
	password := firstNonEmpty(
		registryEnv(env, "DOCKER_PASSWORD"),
		registryEnv(env, "DOCKER_TOKEN"),
		registryEnv(env, "REGISTRY_PASSWORD"),
		registryEnv(env, "REGISTRY_TOKEN"),
		registryEnv(env, "GHCR_TOKEN"),
		registryEnv(env, "GHCR_PAT"),
		registryEnv(env, "CR_PAT"),
		registryEnv(env, "GITHUB_TOKEN"),
		registryEnv(env, "GITHUB_PAT"),
		registryEnv(env, "GITHUB_PACKAGES_TOKEN"),
		registryEnv(env, "PACKAGE_REGISTRY_TOKEN"),
		registryEnv(env, "CONTAINER_REGISTRY_TOKEN"),
		registryEnv(env, "REGISTRY_PAT"),
	)

	if username == "" || password == "" {
		return map[string]any{}
	}

	auth := map[string]any{
		"username": username,
		"password": password,
	}
	if registry != "" {
		auth["registry_url"] = registry
	}
	return auth
}

func registryEnv(env map[string]string, key string) string {
	if v := env[key]; v != "" {
		return v
	}
	return os.Getenv(key)
}

func composeRegistryAuthRequired(image string) bool {
	return strings.HasPrefix(image, "ghcr.io/livepeer-frameworks/")
}

func serviceNativeVars(ctx context.Context, cfg ServiceRoleConfig, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (map[string]any, error) {
	url, checksum, binaryName, artifactVersion, err := resolveGenericBinary(ctx, cfg.ServiceName, host, config, helpers)
	if err != nil {
		return nil, err
	}
	port := config.Port
	if port == 0 {
		port = cfg.DefaultPort
	}
	envMap := buildServiceEnvMap(config)
	if cfg.ServiceName == "livepeer-gateway" || cfg.ServiceName == "livepeer-signer" {
		applyLivepeerNativeEnvDefaults(envMap, cfg.StateDirs)
	}
	files := []map[string]string{}
	livepeerKeystorePath := ""
	livepeerKeystoreDir := ""
	if cfg.ServiceName == "livepeer-gateway" || cfg.ServiceName == "livepeer-signer" {
		var err error
		files, livepeerKeystorePath, livepeerKeystoreDir, err = livepeerNativeFiles(envMap, cfg.StateDirs)
		if err != nil {
			return nil, err
		}
	}
	if cfg.ServiceName == "skipper" {
		skipperFiles, err := skipperNativeSourceFiles(envMap)
		if err != nil {
			return nil, err
		}
		files = append(files, skipperFiles...)
	}
	envAny := make(map[string]any, len(envMap))
	for k, v := range envMap {
		envAny[k] = v
	}
	args := cfg.Args
	if cfg.ServiceName == "livepeer-gateway" || cfg.ServiceName == "livepeer-signer" {
		args = livepeerNativeArgs(cfg.ServiceName, envMap, cfg.StateDirs)
	}
	validateTimeout := 15
	if cfg.ServiceName == "livepeer-gateway" {
		validateTimeout = 120
	}
	vars := map[string]any{
		"go_service_name":                             cfg.ServiceName,
		"go_service_artifact_url":                     url,
		"go_service_artifact_checksum":                checksum,
		"go_service_version":                          firstNonEmpty(artifactVersion, config.Version, metaString(config.Metadata, "version")),
		"go_service_port":                             port,
		"go_service_validate_timeout":                 validateTimeout,
		"go_service_env":                              envAny,
		"go_service_args":                             nonNilStringSlice(args),
		"go_service_files":                            files,
		"go_service_defer_start":                      config.DeferStart,
		"go_service_binary_name":                      binaryName,
		"go_service_runtime_packages":                 nonNilStringSlice(cfg.RuntimePackages),
		"go_service_debian_runtime_packages":          nonNilStringSlice(cfg.DebianRuntimePackages),
		"go_service_pacman_runtime_packages":          nonNilStringSlice(cfg.PacmanRuntimePackages),
		"go_service_state_dirs":                       nonNilStringSlice(cfg.StateDirs),
		"go_service_livepeer_expected_keystore_path":  livepeerKeystorePath,
		"go_service_livepeer_expected_keystore_dir":   livepeerKeystoreDir,
		"go_service_livepeer_expected_wallet_address": envMap["eth_acct_addr"],
		"go_service_data_migrations_marker":           dataMigrationsMarker(cfg, config),
		"go_service_supports_sighup_reload":           servicedefs.SupportsSIGHUPReload(cfg.ServiceName),
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

const (
	skipperSourceMountPath = "/etc/skipper"
	skipperSourceLocalPath = "skipper"
)

func skipperNativeSourceFiles(env map[string]string) ([]map[string]string, error) {
	files, err := readSkipperSourceFiles(func(rel, content string) map[string]string {
		return map[string]string{
			"path":    filepath.Join(skipperSourceMountPath, rel),
			"content": content,
			"mode":    "0644",
		}
	})
	if err != nil {
		return nil, err
	}
	if len(files) > 0 && env["SKIPPER_SITEMAPS_DIR"] == "" {
		env["SKIPPER_SITEMAPS_DIR"] = filepath.Join(skipperSourceMountPath, "sitemaps")
	}
	return files, nil
}

func skipperComposeSourceFiles(env map[string]string) (map[string]string, []string, error) {
	files, err := readSkipperSourceFiles(func(rel, content string) map[string]string {
		return map[string]string{
			"path":    filepath.Join(skipperSourceLocalPath, rel),
			"content": content,
		}
	})
	if err != nil {
		return nil, nil, err
	}
	out := make(map[string]string, len(files))
	for _, file := range files {
		out[file["path"]] = file["content"]
	}
	if len(out) == 0 {
		return out, nil, nil
	}
	if env["SKIPPER_SITEMAPS_DIR"] == "" {
		env["SKIPPER_SITEMAPS_DIR"] = filepath.Join(skipperSourceMountPath, "sitemaps")
	}
	return out, []string{"./" + skipperSourceLocalPath + ":" + skipperSourceMountPath + ":ro"}, nil
}

func readSkipperSourceFiles(mapFile func(rel, content string) map[string]string) ([]map[string]string, error) {
	root, err := findRepoRoot()
	if err != nil {
		return nil, fmt.Errorf("skipper source files: %w", err)
	}
	base := filepath.Join(root, "config", "skipper")
	var files []map[string]string
	for _, dir := range []string{"sitemaps", "faq"} {
		absDir := filepath.Join(base, dir)
		if _, err := os.Stat(absDir); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat %s: %w", absDir, err)
		}
		if err := filepath.WalkDir(absDir, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(base, path)
			if err != nil {
				return err
			}
			files = append(files, mapFile(filepath.ToSlash(rel), string(content)))
			return nil
		}); err != nil {
			return nil, fmt.Errorf("read %s: %w", absDir, err)
		}
	}
	return files, nil
}

func findRepoRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "config", "skipper")); err == nil {
			return wd, nil
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return "", fmt.Errorf("could not find config/skipper from %s", wd)
		}
		wd = parent
	}
}

func dataMigrationsMarker(cfg ServiceRoleConfig, config ServiceConfig) string {
	if !cfg.DataMigrations && !metaBool(config.Metadata, "data_migrations", false) {
		return ""
	}
	runtimeName := firstNonEmpty(config.DeployName, cfg.ServiceName)
	return datamigrate.AdoptionMarkerPath(runtimeName)
}

func nonNilStringSlice(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func applyLivepeerNativeEnvDefaults(env map[string]string, stateDirs []string) {
	if len(stateDirs) == 0 || stateDirs[0] == "" {
		return
	}
	if env["LP_DATADIR"] == "" {
		env["LP_DATADIR"] = stateDirs[0]
	}
	if env["HOME"] == "" {
		env["HOME"] = stateDirs[0]
	}
}

func livepeerNativeFiles(env map[string]string, stateDirs []string) ([]map[string]string, string, string, error) {
	if len(stateDirs) == 0 || stateDirs[0] == "" {
		return nil, "", "", nil
	}

	stateDir := stateDirs[0]
	files := []map[string]string{}
	keystorePath := ""
	keystoreDir := ""
	if env["keystore_path"] == "" && env["LIVEPEER_ETH_KEYSTORE_B64"] != "" {
		content, err := base64.StdEncoding.DecodeString(env["LIVEPEER_ETH_KEYSTORE_B64"])
		if err != nil {
			return nil, "", "", fmt.Errorf("livepeer keystore: decode LIVEPEER_ETH_KEYSTORE_B64: %w", err)
		}
		path := filepath.Join(stateDir, "keystore", "key.json")
		env["keystore_path"] = path
		keystorePath = path
		keystoreDir = filepath.Dir(path)
		files = append(files, map[string]string{
			"path":    path,
			"content": string(content),
			"mode":    "0600",
		})
		delete(env, "LIVEPEER_ETH_KEYSTORE_B64")
	}
	if env["eth_password"] == "" && env["LIVEPEER_ETH_KEYSTORE_PASSWORD"] != "" {
		path := filepath.Join(stateDir, "eth-password")
		env["eth_password"] = path
		files = append(files, map[string]string{
			"path":    path,
			"content": env["LIVEPEER_ETH_KEYSTORE_PASSWORD"],
			"mode":    "0600",
		})
		delete(env, "LIVEPEER_ETH_KEYSTORE_PASSWORD")
	}
	return files, keystorePath, keystoreDir, nil
}

func livepeerNativeArgs(serviceName string, env map[string]string, stateDirs []string) []string {
	args := []string{}
	switch serviceName {
	case "livepeer-gateway":
		args = append(args, "-gateway")
	case "livepeer-signer":
		args = append(args, "-remoteSigner")
	}

	if env["LP_DATADIR"] == "" && len(stateDirs) > 0 && stateDirs[0] != "" {
		args = append(args, "-dataDir="+stateDirs[0])
	} else if v := env["LP_DATADIR"]; v != "" {
		args = append(args, "-dataDir="+v)
	} else if v := firstNonEmpty(env["data_dir"], env["datadir"]); v != "" {
		args = append(args, "-dataDir="+v)
	}

	for _, mapping := range []struct {
		envKey string
		flag   string
	}{
		{"network", "network"},
		{"http_addr", "httpAddr"},
		{"http_ingest", "httpIngest"},
		{"cli_addr", "cliAddr"},
		{"rtmp_addr", "rtmpAddr"},
		{"remote_signer_url", "remoteSignerUrl"},
		{"auth_webhook_url", "authWebhookUrl"},
		{"gateway_host", "gatewayHost"},
		{"max_sessions", "maxSessions"},
		{"max_price_per_unit", "maxPricePerUnit"},
		{"pixels_per_unit", "pixelsPerUnit"},
		{"max_ticket_ev", "maxTicketEV"},
		{"deposit_multiplier", "depositMultiplier"},
		{"block_polling_interval", "blockPollingInterval"},
		{"monitor", "monitor"},
		{"eth_url", "ethUrl"},
		{"eth_acct_addr", "ethAcctAddr"},
		{"orch_webhook_url", "orchWebhookUrl"},
		{"remote_discovery", "remoteDiscovery"},
		{"keystore_path", "ethKeystorePath"},
		{"eth_password", "ethPassword"},
	} {
		if value, ok := env[mapping.envKey]; ok {
			args = append(args, "-"+mapping.flag+"="+value)
		}
	}

	return args
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

func resolveGenericBinary(ctx context.Context, serviceName string, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (string, string, string, string, error) {
	if config.BinaryURL != "" {
		return config.BinaryURL, "", serviceName, firstNonEmpty(config.Version, metaString(config.Metadata, "version")), nil
	}
	channel, version := gitops.ResolveVersion(config.Version)
	manifest, err := fetchGitopsManifest(channel, version, config.Metadata)
	if err != nil {
		return "", "", "", "", err
	}
	remoteOS, remoteArch, err := helpers.DetectRemoteOS(ctx, host)
	if err != nil {
		return "", "", "", "", fmt.Errorf("detect arch: %w", err)
	}
	svc, err := manifest.GetServiceInfo(serviceName)
	if err == nil {
		bin, binErr := svc.GetBinary(remoteOS, remoteArch)
		if binErr != nil {
			return "", "", "", "", binErr
		}
		return bin.URL, bin.Checksum, serviceName, svc.Version, nil
	}
	if bin, depName := binaryFromExternalDependency(serviceName, remoteOS, remoteArch, manifest); bin != nil {
		dep := manifest.GetExternalDependency(depName)
		depVersion := ""
		if dep != nil {
			depVersion = dep.ReleaseTag
		}
		return bin.URL, bin.Checksum, "livepeer", depVersion, nil
	} else if depName != "" {
		return "", "", "", "", fmt.Errorf("external dependency %s has no %s-%s binary for %s", depName, remoteOS, remoteArch, serviceName)
	}
	return "", "", "", "", err
}

func serviceRoleDetect(serviceName string) RoleDetector {
	return func(ctx context.Context, host inventory.Host, _ ServiceConfig, helpers RoleBuildHelpers) (*detect.ServiceState, error) {
		if serviceName == "" || helpers.SSHPool == nil {
			return &detect.ServiceState{Exists: false, Running: false}, nil
		}
		return detect.NewDetector(helpers.SSHPool, host).Detect(ctx, serviceName)
	}
}

// serviceRoleFingerprint produces a desired-state fingerprint for native
// go_service-backed services. The rendered bytes mirror the go_service role's
// env-file and systemd-unit templates so the diff classifier compares the
// files the role actually owns, not a parallel approximation.
func serviceRoleFingerprint(cfg ServiceRoleConfig) RoleFingerprinter {
	return func(ctx context.Context, host inventory.Host, config ServiceConfig, helpers RoleBuildHelpers) (*detect.Fingerprint, error) {
		if cfg.ServiceName == "" {
			return nil, nil
		}
		if config.Mode != "" && config.Mode != "native" {
			return nil, nil
		}

		vars, err := serviceNativeVars(ctx, cfg, host, config, helpers)
		if err != nil {
			return nil, fmt.Errorf("fingerprint %s: render native vars: %w", cfg.ServiceName, err)
		}

		artifactURL := stringFromVars(vars, "go_service_artifact_url")
		artifactChecksum := stringFromVars(vars, "go_service_artifact_checksum")
		if artifactURL == "" || artifactChecksum == "" {
			return nil, fmt.Errorf("binary artifact identity for %s is incomplete", cfg.ServiceName)
		}

		serviceName := stringFromVars(vars, "go_service_name")
		if serviceName == "" {
			serviceName = cfg.ServiceName
		}
		env := stringMapFromAny(vars["go_service_env"])
		args := stringSliceFromAny(vars["go_service_args"])
		supportsReload := boolFromVars(vars, "go_service_supports_sighup_reload")

		files := map[detect.FileKind]detect.ExpectedFile{
			detect.FileKindBinary: {
				Path:   goServiceInstallSentinelPath(serviceName, artifactChecksum, artifactURL),
				SHA256: sha256Hex(""),
			},
			detect.FileKindEnv: {
				Path:   "/etc/frameworks/" + serviceName + ".env",
				SHA256: sha256Hex(renderGoServiceEnvFile(env)),
			},
			detect.FileKindUnit: {
				Path:   "/etc/systemd/system/frameworks-" + serviceName + ".service",
				SHA256: sha256Hex(renderGoServiceUnit(serviceName, args, supportsReload)),
			},
		}

		return &detect.Fingerprint{
			ServiceName: serviceName,
			Host:        host.Name,
			Files:       files,
			ComputedAt:  time.Now(),
		}, nil
	}
}

func goServiceInstallSentinelPath(serviceName, checksum, artifactURL string) string {
	identity := checksum + ":" + artifactURL
	return "/opt/frameworks/" + serviceName + "/.installed-" + sha256Hex(identity)
}

func renderGoServiceEnvFile(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	for _, key := range keys {
		value := strings.ReplaceAll(env[key], "\r", "")
		value = strings.ReplaceAll(value, "\n", `\n`)
		b.WriteString(key)
		b.WriteByte('=')
		b.WriteString(shellQuoteForAnsible(value))
		b.WriteByte('\n')
	}
	return b.String()
}

func renderGoServiceUnit(serviceName string, args []string, supportsReload bool) string {
	installDir := "/opt/frameworks/" + serviceName
	argv := append([]string{installDir + "/" + serviceName}, args...)
	quoted := make([]string, 0, len(argv))
	for _, arg := range argv {
		quoted = append(quoted, shellQuoteForAnsible(arg))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "[Unit]\n")
	fmt.Fprintf(&b, "Description=Frameworks %s\n", serviceName)
	fmt.Fprintf(&b, "After=network-online.target\n")
	fmt.Fprintf(&b, "Wants=network-online.target\n\n")
	fmt.Fprintf(&b, "[Service]\n")
	fmt.Fprintf(&b, "User=frameworks\n")
	fmt.Fprintf(&b, "Group=frameworks\n")
	fmt.Fprintf(&b, "WorkingDirectory=%s\n", installDir)
	fmt.Fprintf(&b, "EnvironmentFile=/etc/frameworks/%s.env\n", serviceName)
	fmt.Fprintf(&b, "ExecStart=%s\n", strings.Join(quoted, " "))
	if supportsReload {
		fmt.Fprintf(&b, "ExecReload=/bin/kill -HUP $MAINPID\n")
	}
	fmt.Fprintf(&b, "Restart=always\n")
	fmt.Fprintf(&b, "RestartSec=5\n")
	fmt.Fprintf(&b, "LimitNOFILE=1048576\n\n")
	fmt.Fprintf(&b, "[Install]\n")
	fmt.Fprintf(&b, "WantedBy=multi-user.target\n")
	return b.String()
}

func shellQuoteForAnsible(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return (r < 'A' || r > 'Z') &&
			(r < 'a' || r > 'z') &&
			(r < '0' || r > '9') &&
			!strings.ContainsRune("_@%+=:,./-", r)
	}) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("%x", sum[:])
}

func stringFromVars(vars map[string]any, key string) string {
	if v, ok := vars[key].(string); ok {
		return v
	}
	return ""
}

func boolFromVars(vars map[string]any, key string) bool {
	if v, ok := vars[key].(bool); ok {
		return v
	}
	return false
}

func stringMapFromAny(v any) map[string]string {
	switch m := v.(type) {
	case map[string]string:
		out := make(map[string]string, len(m))
		maps.Copy(out, m)
		return out
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, value := range m {
			out[k] = fmt.Sprint(value)
		}
		return out
	default:
		return map[string]string{}
	}
}

func stringSliceFromAny(v any) []string {
	switch s := v.(type) {
	case []string:
		out := make([]string, len(s))
		copy(out, s)
		return out
	case []any:
		out := make([]string, 0, len(s))
		for _, item := range s {
			out = append(out, fmt.Sprint(item))
		}
		return out
	default:
		return nil
	}
}
