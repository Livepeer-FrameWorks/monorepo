package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"frameworks/cli/pkg/detect"
	serviceexec "frameworks/cli/pkg/exec"
	"frameworks/cli/pkg/gitops"
	"frameworks/cli/pkg/ssh"
	"frameworks/cli/pkg/system"
	"github.com/spf13/cobra"
)

var releaseDataPreflightFn = runReleaseDataPreflight
var preflightChecksum = regexp.MustCompile(`^sha256:[a-fA-F0-9]{64}$`)

func runReleaseDataPreflight(ctx context.Context, cmd *cobra.Command, rc *resolvedCluster, pool *ssh.Pool, release *gitops.Manifest) error {
	if strings.Split(release.PlatformVersion, "-")[0] != "v0.3.14" {
		return nil
	}
	host, ok := manifestHostFor(rc.Manifest)("purser")
	if !ok {
		return nil
	}
	info, err := release.GetServiceInfo("purser")
	if err != nil {
		return fmt.Errorf("purser data preflight unavailable: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	state, err := detect.NewDetector(pool, host).Detect(ctx, "purser")
	if err != nil {
		return err
	}
	runner, err := getRunner(host, pool)
	if err != nil {
		return err
	}
	var script string
	switch state.Mode {
	case "docker":
		if !strings.Contains(info.FullImage, "@sha256:") {
			return fmt.Errorf("purser preflight requires a digest-pinned target image")
		}
		container := state.Metadata["container_name"]
		if container == "" {
			return fmt.Errorf("purser container not found")
		}
		script = dockerDataPreflightScript(container, info.FullImage)
	case "native":
		result, archErr := runner.Run(ctx, "uname -m")
		if archErr != nil {
			return archErr
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("detect Purser host architecture")
		}
		arch := ""
		switch strings.TrimSpace(result.Stdout) {
		case "x86_64":
			arch = "amd64"
		case "aarch64", "arm64":
			arch = "arm64"
		default:
			return fmt.Errorf("unsupported Purser host architecture")
		}
		artifact, artifactErr := info.GetBinary("linux", arch)
		if artifactErr != nil {
			return artifactErr
		}
		script, err = nativeDataPreflightScript(*artifact, serviceexec.SpecFromDetection(state.Mode, state.Metadata, "purser"))
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("purser data preflight unavailable: runtime %q", state.Mode)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "\nPurser release data preflight (isolated target artifact; read-only database):")
	result, err := runner.RunScript(ctx, script)
	if err != nil {
		return fmt.Errorf("purser data preflight: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("purser data preflight unavailable (exit %d); target artifact may not support release-preflight", result.ExitCode)
	}
	var report struct {
		Release    string `json:"release"`
		ObservedAt string `json:"observed_at"`
		Checks     []struct {
			ID, Status, Effect string
			Total              int64
			Rows               []struct {
				TenantID string `json:"tenant_id"`
				ID       string `json:"id"`
			}
		} `json:"checks"`
	}
	if err := json.Unmarshal([]byte(result.Stdout), &report); err != nil {
		return fmt.Errorf("decode Purser data preflight: %w", err)
	}
	if report.Release != "v0.3.14" || len(report.Checks) == 0 {
		return fmt.Errorf("purser returned no checks for v0.3.14")
	}
	fmt.Fprintf(cmd.OutOrStdout(), "  observed_at=%s release=%s\n", report.ObservedAt, report.Release)
	incomplete := false
	for _, check := range report.Checks {
		fmt.Fprintf(cmd.OutOrStdout(), "  %s: %s; affected=%d shown=%d; %s\n", check.ID, check.Status, check.Total, len(check.Rows), check.Effect)
		if check.Status != "checked" {
			incomplete = true
		}
		for _, row := range check.Rows {
			fmt.Fprintf(cmd.OutOrStdout(), "    tenant=%s row=%s\n", row.TenantID, row.ID)
		}
	}
	if incomplete {
		return fmt.Errorf("purser data preflight incomplete; at least one check is unavailable")
	}
	return nil
}

func nativeDataPreflightScript(artifact gitops.Artifact, spec serviceexec.Spec) (string, error) {
	if !preflightChecksum.MatchString(artifact.Checksum) || !strings.HasPrefix(artifact.URL, "https://") {
		return "", fmt.Errorf("purser preflight requires an HTTPS artifact with a SHA-256 checksum")
	}
	if artifact.Arch != "linux-amd64" && artifact.Arch != "linux-arm64" {
		return "", fmt.Errorf("purser preflight requires a supported Linux binary architecture")
	}
	member := "frameworks-purser-" + artifact.Arch
	// The service account reads its existing env; no install path, service unit,
	// or database state is changed by this temporary target binary.
	inner := `set -euo pipefail
umask 077
preflight_tmp=$(mktemp -d)
trap 'rm -rf "$preflight_tmp"' EXIT
curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' --max-time 120 ` + ssh.ShellQuote(artifact.URL) + ` -o "$preflight_tmp/artifact.tar.gz"
printf '%s  %s\n' ` + ssh.ShellQuote(strings.TrimPrefix(artifact.Checksum, "sha256:")) + ` "$preflight_tmp/artifact.tar.gz" | sha256sum --check --status
tar -xOzf "$preflight_tmp/artifact.tar.gz" ` + ssh.ShellQuote(member) + ` > "$preflight_tmp/purser"
chmod 700 "$preflight_tmp/purser"
set -a
. ` + ssh.ShellQuote(spec.EnvFile) + `
set +a
cd ` + ssh.ShellQuote(spec.WorkingDirectory) + `
"$preflight_tmp/purser" release-preflight
`
	return "sudo -n -u " + ssh.ShellQuote(spec.User) + " /bin/bash -c " + ssh.ShellQuote(inner), nil
}

func dockerDataPreflightScript(container, image string) string {
	return `set -euo pipefail
umask 077
preflight_tmp=$(mktemp -d)
trap 'rm -rf "$preflight_tmp"' EXIT
` + system.DockerCommand("inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "+ssh.ShellQuote(container)) + ` | sed -n '/^DATABASE_URL=/p' > "$preflight_tmp/env"
` + system.DockerCommand("run --rm --read-only --cap-drop ALL --security-opt no-new-privileges --network "+ssh.ShellQuote("container:"+container)+` --env-file "$preflight_tmp/env" --entrypoint /app/purser `+ssh.ShellQuote(image)+" release-preflight") + `
`
}
