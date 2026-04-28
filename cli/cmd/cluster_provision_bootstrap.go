package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"

	"frameworks/cli/pkg/bootstrap"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/remoteaccess"
	"frameworks/cli/pkg/ssh"
	"frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/logging"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// renderBootstrapYAML produces the rendered desired-state document handed to
// the per-service `<service> bootstrap` subcommands. It threads the operator
// CLI flags (--bootstrap-admin-*) into a system_operator account that
// commodore bootstrap turns into the initial user.
//
// The returned bytes are the full multi-section document (quartermaster,
// purser, accounts); each service decodes only its own section.
func renderBootstrapYAML(cmd *cobra.Command, manifest *inventory.Manifest, manifestDir string, sharedEnv map[string]string) ([]byte, error) {
	opts := bootstrap.DeriveOptions{
		SharedEnv: sharedEnv,
	}

	if email, _ := cmd.Flags().GetString("bootstrap-admin-email"); email != "" { //nolint:errcheck // flag always exists
		ref, err := bootstrapAdminPasswordRef(cmd)
		if err != nil {
			return nil, err
		}
		firstName, _ := cmd.Flags().GetString("bootstrap-admin-first-name") //nolint:errcheck
		lastName, _ := cmd.Flags().GetString("bootstrap-admin-last-name")   //nolint:errcheck
		opts.BootstrapAdmin = &bootstrap.BootstrapAdminSpec{
			Email:       email,
			Role:        "owner",
			FirstName:   firstName,
			LastName:    lastName,
			PasswordRef: ref,
		}
	}

	resolver := &bootstrap.DefaultResolver{
		BaseDir:    manifestDir,
		AgeKeyFile: os.Getenv("SOPS_AGE_KEY_FILE"),
		Flags:      bootstrapAdminFlagValues(cmd),
	}

	rendered, err := bootstrap.RenderFromManifest(manifest, manifestDir, opts, resolver)
	if err != nil {
		return nil, fmt.Errorf("render bootstrap desired-state: %w", err)
	}
	if err := rendered.Validate(); err != nil {
		return nil, fmt.Errorf("validate bootstrap desired-state: %w", err)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(rendered); err != nil {
		return nil, fmt.Errorf("encode bootstrap desired-state: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("encode bootstrap desired-state: %w", err)
	}
	return buf.Bytes(), nil
}

// bootstrapAdminPasswordRef picks the first non-empty password source and
// returns it as a SecretRef. The flag-passing path goes through the
// DefaultResolver's flag map below; the env path returns an env-shaped ref;
// the file path returns a file-shaped ref. --bootstrap-admin-email without a
// password source is a hard error.
func bootstrapAdminPasswordRef(cmd *cobra.Command) (bootstrap.SecretRef, error) {
	if pwFile, _ := cmd.Flags().GetString("bootstrap-admin-password-file"); pwFile != "" { //nolint:errcheck
		return bootstrap.SecretRef{File: pwFile}, nil
	}
	if pwEnv, _ := cmd.Flags().GetString("bootstrap-admin-password-env"); pwEnv != "" { //nolint:errcheck
		return bootstrap.SecretRef{Env: pwEnv}, nil
	}
	if pw, _ := cmd.Flags().GetString("bootstrap-admin-password"); pw != "" { //nolint:errcheck
		return bootstrap.SecretRef{Flag: "bootstrap-admin-password"}, nil
	}
	return bootstrap.SecretRef{}, fmt.Errorf("--bootstrap-admin-email requires a password (use --bootstrap-admin-password, --bootstrap-admin-password-env, or --bootstrap-admin-password-file)")
}

// bootstrapAdminFlagValues collects flag values the resolver may need to
// resolve a Flag-shaped SecretRef. Today only --bootstrap-admin-password.
func bootstrapAdminFlagValues(cmd *cobra.Command) map[string]string {
	out := map[string]string{}
	if pw, _ := cmd.Flags().GetString("bootstrap-admin-password"); pw != "" { //nolint:errcheck
		out["bootstrap-admin-password"] = pw
	}
	return out
}

// runServiceBootstrap is the cluster_provision-side entry point that uploads
// the rendered desired-state YAML and runs `<service> bootstrap` on the host
// owning that service. It picks the host from the manifest's services map.
func runServiceBootstrap(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, sshPool *ssh.Pool, service string, yamlBytes []byte, extraArgs []string) error {
	host, err := serviceHostForBootstrap(manifest, service)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  Running %s bootstrap on %s...\n", service, host.Name)
	return provisioner.RunServiceBootstrap(ctx, sshPool, provisioner.ServiceBootstrapOptions{
		Service:   service,
		Host:      host,
		Mode:      provisioner.ServiceBootstrapModeApply,
		YAML:      string(yamlBytes),
		ExtraArgs: extraArgs,
	})
}

// runServiceBootstrapValidate runs `<service> bootstrap validate` — the
// post-apply cross-service invariant check. Today only purser uses it (every
// platform-official cluster must have a cluster_pricing row).
func runServiceBootstrapValidate(ctx context.Context, cmd *cobra.Command, manifest *inventory.Manifest, sshPool *ssh.Pool, service string) error {
	host, err := serviceHostForBootstrap(manifest, service)
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  Running %s bootstrap validate on %s...\n", service, host.Name)
	return provisioner.RunServiceBootstrap(ctx, sshPool, provisioner.ServiceBootstrapOptions{
		Service: service,
		Host:    host,
		Mode:    provisioner.ServiceBootstrapModeValidate,
	})
}

// serviceHostForBootstrap returns the host that runs the named service. A
// service may declare Host (single) or Hosts (multi); for bootstrap we pick
// the first one — the bootstrap subcommand connects to the shared DB and is
// idempotent, so running it on any one host that has the binary is correct.
func serviceHostForBootstrap(manifest *inventory.Manifest, service string) (inventory.Host, error) {
	svc, ok := manifest.Services[service]
	if !ok {
		return inventory.Host{}, fmt.Errorf("service %q not declared in manifest", service)
	}
	hostKey := svc.Host
	if hostKey == "" && len(svc.Hosts) > 0 {
		hostKey = svc.Hosts[0]
	}
	if hostKey == "" {
		return inventory.Host{}, fmt.Errorf("service %q has no Host or Hosts in manifest", service)
	}
	host, ok := manifest.GetHost(hostKey)
	if !ok {
		return inventory.Host{}, fmt.Errorf("service %q references host %q not in manifest", service, hostKey)
	}
	return host, nil
}

// resolveSystemTenantIDViaQM asks Quartermaster (which has just finished
// running its own bootstrap) for the system tenant's UUID. Used to populate
// runtimeData["system_tenant_id"] for the readiness report and for
// bootstrap-admin user creation. Alias→UUID is QM-owned data; the CLI never
// reads quartermaster.bootstrap_tenant_aliases directly.
func resolveSystemTenantIDViaQM(ctx context.Context, manifest *inventory.Manifest, runtimeData map[string]any, sess *remoteaccess.Session) (string, error) {
	serviceToken, grpcAddr, serverName, insecure, err := quartermasterDialEndpoint(ctx, manifest, runtimeData, sess)
	if err != nil {
		return "", err
	}
	clientConfig := quartermaster.GRPCConfig{
		GRPCAddr:      grpcAddr,
		Timeout:       quartermasterRPCTimeout,
		Logger:        logging.NewLogger(),
		ServiceToken:  serviceToken,
		AllowInsecure: insecure,
		ServerName:    serverName,
	}
	if pki, ok := runtimeData["internal_pki_bootstrap"].(*internalPKIBootstrap); ok && pki != nil {
		clientConfig.CACertPEM = pki.CABundlePEM
	}
	client, err := quartermaster.NewGRPCClient(clientConfig)
	if err != nil {
		return "", fmt.Errorf("connect Quartermaster gRPC: %w", err)
	}
	defer client.Close()

	resp, err := client.ResolveTenantAliases(ctx, []string{bootstrap.SystemTenantAlias})
	if err != nil {
		return "", fmt.Errorf("ResolveTenantAliases: %w", err)
	}
	if len(resp.GetUnknown()) > 0 {
		return "", fmt.Errorf("system tenant alias %q not in QM's bootstrap_tenant_aliases (run quartermaster bootstrap first)", bootstrap.SystemTenantAlias)
	}
	id, ok := resp.GetMapping()[bootstrap.SystemTenantAlias]
	if !ok {
		return "", fmt.Errorf("system tenant alias %q: empty mapping in ResolveTenantAliases response", bootstrap.SystemTenantAlias)
	}
	return id, nil
}

// commodoreBootstrapExtraArgs threads the --bootstrap-reset-credentials flag
// through to `commodore bootstrap`. Mirrors the runtime guard rail: passwords
// are never silently rewritten.
func commodoreBootstrapExtraArgs(cmd *cobra.Command) []string {
	if reset, _ := cmd.Flags().GetBool("bootstrap-reset-credentials"); reset { //nolint:errcheck
		return []string{"--reset-credentials"}
	}
	return nil
}
