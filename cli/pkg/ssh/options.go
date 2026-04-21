package ssh

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"
)

// Resolution is the outcome of ResolveTarget: what string to pass to ssh/scp
// as the destination, plus whether it came from a verified ~/.ssh/config alias.
type Resolution struct {
	// Target is what to pass as the ssh host (e.g. "root@1.2.3.4" for raw IP,
	// or "frameworks-central-eu-1" for a verified alias).
	Target string
	// AliasVerified is true when Target is a Host block alias whose resolved
	// HostName was confirmed to point at ConnectionConfig.Address.
	AliasVerified bool
}

// Resolver resolves ConnectionConfig into a concrete Resolution. It exists as
// an interface so tests can stub out `ssh -G` and DNS lookups.
type Resolver interface {
	Resolve(ctx context.Context, cfg *ConnectionConfig) (Resolution, error)
}

// DefaultResolver runs ssh -G to probe ~/.ssh/config and net.DefaultResolver
// to expand DNS names. Safe for concurrent use.
type DefaultResolver struct {
	// SSHGHostname, if set, is called instead of running `ssh -G`. Tests inject this.
	SSHGHostname func(ctx context.Context, alias string) (string, error)
	// LookupHost, if set, is called instead of net.DefaultResolver.LookupHost. Tests inject this.
	LookupHost func(ctx context.Context, name string) ([]string, error)
}

// Resolve implements the candidate-and-verify flow from the SSH redesign plan.
func (r *DefaultResolver) Resolve(ctx context.Context, cfg *ConnectionConfig) (Resolution, error) {
	if cfg == nil {
		return Resolution{}, fmt.Errorf("nil ConnectionConfig")
	}
	if cfg.HostName != "" {
		candidates := []string{cfg.HostName, "frameworks-" + cfg.HostName}
		for _, c := range candidates {
			if c == cfg.Address {
				// Skip: candidate is literally the IP; no alias lookup needed.
				continue
			}
			verified, err := r.verifyAlias(ctx, c, cfg.Address)
			if err != nil || !verified {
				continue
			}
			return Resolution{Target: c, AliasVerified: true}, nil
		}
	}
	if cfg.User == "" {
		return Resolution{}, fmt.Errorf("no verified ~/.ssh/config alias for %s and no user set: populate `user:` in the manifest or add a Host block", cfg.Address)
	}
	return Resolution{
		Target:        fmt.Sprintf("%s@%s", cfg.User, cfg.Address),
		AliasVerified: false,
	}, nil
}

// verifyAlias runs `ssh -G <alias>`, extracts the resolved hostname, and
// checks whether it points at the manifest's Address. DNS names are expanded
// and compared by IP-set intersection.
func (r *DefaultResolver) verifyAlias(ctx context.Context, alias, wantAddr string) (bool, error) {
	host, err := r.sshGHostname(ctx, alias)
	if err != nil || host == "" {
		return false, err
	}
	// If ssh -G returned exactly the alias (no Host block matched), not verified.
	if host == alias {
		return false, nil
	}
	if host == wantAddr {
		return true, nil
	}
	// Literal IP mismatch, or DNS name — resolve and compare.
	if net.ParseIP(host) != nil {
		return false, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	addrs, err := r.lookupHost(lookupCtx, host)
	if err != nil {
		return false, nil // safety over convenience: unknown = not verified
	}
	for _, a := range addrs {
		if a == wantAddr {
			return true, nil
		}
	}
	return false, nil
}

func (r *DefaultResolver) sshGHostname(ctx context.Context, alias string) (string, error) {
	if r.SSHGHostname != nil {
		return r.SSHGHostname(ctx, alias)
	}
	cmd := exec.CommandContext(ctx, "ssh", "-G", alias)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return parseSSHGHostname(string(out)), nil
}

func (r *DefaultResolver) lookupHost(ctx context.Context, name string) ([]string, error) {
	if r.LookupHost != nil {
		return r.LookupHost(ctx, name)
	}
	return net.DefaultResolver.LookupHost(ctx, name)
}

// parseSSHGHostname extracts the `hostname` directive from `ssh -G` output.
// `ssh -G` lowercases all keys and emits them as `key value` lines.
func parseSSHGHostname(out string) string {
	for line := range strings.SplitSeq(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		if len(fields) != 2 {
			continue
		}
		if fields[0] == "hostname" {
			return strings.TrimSpace(fields[1])
		}
	}
	return ""
}

// BuildSSHArgs returns the argv prefix for `ssh`, excluding the target and the
// command. Caller appends `res.Target`, optional `sh`, `-lc`, <remote-cmd>.
//
// Policy:
//   - KeyPath → -i <path>, applied regardless of AliasVerified (explicit key wins).
//   - User is NEVER emitted as -l. It's either encoded in res.Target (raw IP)
//     or deliberately omitted so the alias's User directive wins.
//   - Default: StrictHostKeyChecking=accept-new. Writes unknown keys to
//     ~/.ssh/known_hosts; rejects mismatches.
//   - InsecureSkipVerify → StrictHostKeyChecking=no + UserKnownHostsFile=/dev/null.
//   - KnownHostsPath → UserKnownHostsFile=<path>.
//   - Port → -p <n> only when non-default.
func BuildSSHArgs(cfg *ConnectionConfig, _ Resolution) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", fmt.Sprintf("ConnectTimeout=%d", connectTimeoutSeconds(cfg)),
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
	}
	args = append(args, hostKeyCheckingArgs(cfg)...)
	if cfg.KeyPath != "" {
		args = append(args, "-i", cfg.KeyPath)
	}
	if cfg.Port != 0 && cfg.Port != 22 {
		args = append(args, "-p", fmt.Sprintf("%d", cfg.Port))
	}
	return args
}

// connectTimeoutSeconds maps cfg.Timeout to ssh's ConnectTimeout option.
// Defaults to 10s when unset; rounds sub-second values up to 1.
func connectTimeoutSeconds(cfg *ConnectionConfig) int {
	if cfg.Timeout <= 0 {
		return 10
	}
	secs := int(cfg.Timeout.Seconds())
	if secs < 1 {
		return 1
	}
	return secs
}

// BuildSCPArgs returns argv for `scp`, including the final source and
// destination. Caller invokes: exec.Command("scp", BuildSCPArgs(...)...).
func BuildSCPArgs(cfg *ConnectionConfig, res Resolution, local, remote string) []string {
	args := []string{
		"-o", "BatchMode=yes",
		"-o", fmt.Sprintf("ConnectTimeout=%d", connectTimeoutSeconds(cfg)),
	}
	args = append(args, hostKeyCheckingArgs(cfg)...)
	if cfg.KeyPath != "" {
		args = append(args, "-i", cfg.KeyPath)
	}
	if cfg.Port != 0 && cfg.Port != 22 {
		// scp uses -P (capital) for port.
		args = append(args, "-P", fmt.Sprintf("%d", cfg.Port))
	}
	args = append(args, local, fmt.Sprintf("%s:%s", res.Target, remote))
	return args
}

// hostKeyCheckingArgs encodes the known_hosts policy.
func hostKeyCheckingArgs(cfg *ConnectionConfig) []string {
	if cfg.InsecureSkipVerify {
		return []string{
			"-o", "StrictHostKeyChecking=no",
			"-o", "UserKnownHostsFile=/dev/null",
		}
	}
	args := []string{"-o", "StrictHostKeyChecking=accept-new"}
	if cfg.KnownHostsPath != "" {
		args = append(args, "-o", "UserKnownHostsFile="+cfg.KnownHostsPath)
	}
	return args
}
