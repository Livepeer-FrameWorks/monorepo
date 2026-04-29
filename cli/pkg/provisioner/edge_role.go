package provisioner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"frameworks/cli/pkg/ansiblerun"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/ssh"
)

// runEdgeRole is the role-backed install path used by EdgeProvisioner for
// Linux + Darwin hosts. It resolves pinned artifacts for every edge
// sub-service, renders a one-host inventory, and invokes the
// frameworks.infra.edge playbook via ansiblerun. Preflight and HTTPS
// verification stay in Go; sysctl/limits tuning lives in the role under
// the edge_apply_tuning flag. config is passed by pointer so the generated
// MistServer API password persists on the caller's struct — subsequent
// reads of that password (e.g. logging, retry) see the same value.
func runEdgeRole(ctx context.Context, pool *ssh.Pool, host inventory.Host, config *EdgeProvisionConfig, remoteOS, remoteArch string) error {
	vars, err := edgeRoleVars(config, remoteOS, remoteArch)
	if err != nil {
		return err
	}

	// Darwin user-domain deploys run entirely under $HOME. Escalating there
	// would defeat the no-admin contract of local `edge deploy`. Linux and
	// Darwin system-domain still need become for /etc + /Library writes.
	become := remoteOS != "darwin" || config.DarwinDomain != DomainUser
	vars["edge_become"] = become

	root, err := FindAnsibleRoot()
	if err != nil {
		return fmt.Errorf("edge: locate ansible root: %w", err)
	}
	executor, err := ansiblerun.NewExecutor()
	if err != nil {
		return fmt.Errorf("edge: %w", err)
	}
	ensurer := &ansiblerun.CollectionEnsurer{
		RequirementsFile: filepath.Join(root, "requirements.yml"),
	}
	cache, err := ensurer.Ensure(ctx)
	if err != nil {
		return fmt.Errorf("edge: ensure ansible collections + roles: %w", err)
	}

	invDir, err := os.MkdirTemp("", "frameworks-edge-*")
	if err != nil {
		return fmt.Errorf("edge: mkdtemp: %w", err)
	}
	defer os.RemoveAll(invDir)

	hostName := host.Name
	if hostName == "" {
		hostName = "edge"
	}
	address := hostAddressFor(host)
	// `edge deploy` local mode hands in a localhost host with USER from
	// env. Ansible needs connection=local to skip SSH-to-self; otherwise
	// the play fails unless Remote Login is enabled on macOS. The ssh
	// runner in BaseProvisioner already routes localhost to LocalRunner,
	// so the rest of EdgeProvisioner is fine — this just aligns Ansible.
	connection := ""
	privateKey := pool.DefaultKeyPath()
	if address == "localhost" || address == "127.0.0.1" {
		connection = "local"
		privateKey = ""
	}
	renderer := &ansiblerun.InventoryRenderer{}
	invPath, err := renderer.Render(invDir, ansiblerun.Inventory{
		Hosts: []ansiblerun.Host{
			{
				Name:       hostName,
				Address:    address,
				User:       host.User,
				PrivateKey: privateKey,
				Connection: connection,
			},
		},
		Groups: []ansiblerun.Group{{
			Name:  "edge",
			Hosts: []string{hostName},
		}},
		GlobalVars: map[string]any{
			"frameworks_target_group": "edge",
		},
	})
	if err != nil {
		return fmt.Errorf("edge: render inventory: %w", err)
	}

	envVars := map[string]string{
		"ANSIBLE_COLLECTIONS_PATH": cache.CollectionsPath + string(os.PathListSeparator) + filepath.Join(root, "collections"),
		"ANSIBLE_ROLES_PATH":       cache.RolesPath,
	}
	for _, k := range []string{"SOPS_AGE_KEY_FILE", "SOPS_AGE_KEY", "HOME", "USER", "PATH"} {
		if v := os.Getenv(k); v != "" {
			envVars[k] = v
		}
	}

	return executor.Execute(ctx, ansiblerun.ExecuteOptions{
		Playbook:   filepath.Join(root, "playbooks/edge.yml"),
		Inventory:  invPath,
		ExtraVars:  vars,
		Tags:       []string{"install", "configure", "service", "validate"},
		PrivateKey: privateKey,
		User:       host.User,
		Become:     become,
		WorkDir:    root,
		EnvVars:    envVars,
	})
}

// edgeRoleVars builds the extra-vars map the frameworks.infra.edge role
// expects. Pinned artifacts are resolved from the release manifest named by
// config.Version. Native mode without Version errors out because native
// installs require pinned tarballs. Docker mode can run without a release
// selector for local workflows, but uses the pinned MistServer image when one
// is available.
func edgeRoleVars(config *EdgeProvisionConfig, remoteOS, remoteArch string) (map[string]any, error) {
	mode := config.resolvedMode()

	darwinDomain := string(config.DarwinDomain)
	if darwinDomain == "" {
		darwinDomain = string(DomainSystem)
	}

	mistPass, err := ensureEdgeMistPassword(config)
	if err != nil {
		return nil, fmt.Errorf("edge: generate MistServer API password: %w", err)
	}

	vars := map[string]any{
		"edge_mode":              mode,
		"edge_node_id":           config.NodeID,
		"edge_domain":            config.primaryDomain(),
		"edge_acme_email":        config.Email,
		"edge_foghorn_grpc_addr": config.FoghornGRPCAddr,
		"edge_enrollment_token":  config.EnrollmentToken,
		"edge_telemetry_url":     config.TelemetryURL,
		"edge_telemetry_token":   config.TelemetryToken,
		"edge_cert_pem":          config.CertPEM,
		"edge_key_pem":           config.KeyPEM,
		"edge_ca_bundle_pem":     config.CABundlePEM,
		"edge_mist_api_password": mistPass,
		"edge_mistserver_image":  "mistserver:latest",
		"edge_apply_tuning":      config.ApplyTuning && remoteOS != "darwin",
		"edge_darwin_domain":     darwinDomain,
	}

	var manifest *gitops.Manifest
	if strings.TrimSpace(config.Version) != "" {
		var manifestErr error
		manifest, manifestErr = fetchEdgeManifest(config.Version)
		if manifestErr != nil {
			return nil, manifestErr
		}
		image, imageErr := edgeExternalImage(manifest, "mistserver")
		if imageErr != nil {
			return nil, imageErr
		}
		if image != "" {
			vars["edge_mistserver_image"] = image
		}
	}

	if mode != "native" {
		return vars, nil
	}

	if manifest == nil {
		var manifestErr error
		manifest, manifestErr = fetchEdgeManifest(config.Version)
		if manifestErr != nil {
			return nil, manifestErr
		}
	}

	arch := remoteOS + "-" + remoteArch

	mistURL, mistSum, err := edgeExternalBinary(manifest, "mistserver", arch)
	if err != nil {
		return nil, err
	}
	vars["edge_mistserver_artifact_url"] = mistURL
	vars["edge_mistserver_artifact_checksum"] = mistSum

	helmURL, helmSum, err := edgeServiceBinary(manifest, "helmsman", remoteOS, remoteArch)
	if err != nil {
		return nil, err
	}
	vars["edge_helmsman_artifact_url"] = helmURL
	vars["edge_helmsman_artifact_checksum"] = helmSum

	caddyURL, caddySum, err := edgeCaddyBinary(manifest, arch, remoteOS, remoteArch)
	if err != nil {
		return nil, err
	}
	vars["edge_caddy_artifact_url"] = caddyURL
	vars["edge_caddy_artifact_checksum"] = caddySum

	if strings.TrimSpace(config.TelemetryURL) != "" {
		artifact, err := resolveInfraArtifactFromChannel("vmagent", arch, config.Version, nil)
		if err != nil {
			return nil, fmt.Errorf("edge: resolve vmagent artifact: %w", err)
		}
		vars["edge_vmagent_artifact_url"] = artifact.URL
		vars["edge_vmagent_artifact_checksum"] = artifact.Checksum
	}

	return vars, nil
}

func fetchEdgeManifest(version string) (*gitops.Manifest, error) {
	if strings.TrimSpace(version) == "" {
		return nil, fmt.Errorf("edge native provisioning requires --version to resolve binary pins")
	}
	channel, resolved := gitops.ResolveVersion(version)
	fetcher, err := gitops.NewFetcher(gitops.FetchOptions{})
	if err != nil {
		return nil, fmt.Errorf("edge: create gitops fetcher: %w", err)
	}
	manifest, err := fetcher.Fetch(channel, resolved)
	if err != nil {
		return nil, fmt.Errorf("edge: fetch gitops manifest %s/%s: %w", channel, resolved, err)
	}
	return manifest, nil
}

func edgeExternalImage(manifest *gitops.Manifest, name string) (string, error) {
	dep := manifest.GetExternalDependency(name)
	if dep == nil {
		return "", fmt.Errorf("edge: release manifest has no external_dependency entry for %q", name)
	}
	if dep.Image == "" {
		return "", nil
	}
	if dep.Digest == "" {
		return dep.Image, nil
	}
	return dep.Image + "@" + dep.Digest, nil
}

func edgeExternalBinary(manifest *gitops.Manifest, name, arch string) (string, string, error) {
	dep := manifest.GetExternalDependency(name)
	if dep == nil {
		return "", "", fmt.Errorf("edge: release manifest has no external_dependency entry for %q", name)
	}
	for i := range dep.Binaries {
		bin := &dep.Binaries[i]
		if strings.Contains(bin.Name, arch) && bin.URL != "" {
			return bin.URL, bin.Checksum, nil
		}
	}
	return "", "", fmt.Errorf("edge: release manifest %s entry has no binary URL for arch %q", name, arch)
}

func edgeServiceBinary(manifest *gitops.Manifest, name, remoteOS, remoteArch string) (string, string, error) {
	info, err := manifest.GetServiceInfo(name)
	if err != nil {
		return "", "", fmt.Errorf("edge: resolve %s service info: %w", name, err)
	}
	bin, err := info.GetBinary(remoteOS, remoteArch)
	if err != nil {
		return "", "", fmt.Errorf("edge: %s has no binary for %s/%s: %w", name, remoteOS, remoteArch, err)
	}
	return bin.URL, bin.Checksum, nil
}

// edgeCaddyBinary mirrors edge.go's lookup order: external_dependencies
// first (where Caddy binary pins have historically lived), service_info as
// a fallback for deployments that ship caddy via the service channel.
func edgeCaddyBinary(manifest *gitops.Manifest, arch, remoteOS, remoteArch string) (string, string, error) {
	if dep := manifest.GetExternalDependency("caddy"); dep != nil {
		if bin := dep.GetBinary(arch); bin != nil && bin.URL != "" {
			return bin.URL, bin.Checksum, nil
		}
	}
	return edgeServiceBinary(manifest, "caddy", remoteOS, remoteArch)
}

// ensureEdgeMistPassword returns the shared MistServer API password used by
// mistserver (-a) + helmsman (MIST_API_PASSWORD). Generated lazily on the
// first call per config so docker + native installs see the same value
// within a single Provision invocation. Fails closed — an entropy error
// aborts the install rather than installing a predictable credential.
func ensureEdgeMistPassword(config *EdgeProvisionConfig) (string, error) {
	if config.mistPassword != "" {
		return config.mistPassword, nil
	}
	pass, err := generateEdgePassword()
	if err != nil {
		return "", err
	}
	config.mistPassword = pass
	return pass, nil
}
