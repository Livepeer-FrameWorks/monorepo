package detect

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"frameworks/cli/pkg/inventory"
	fwssh "frameworks/cli/pkg/ssh"
)

// FileKind names the kind of file a service exposes for fingerprinting.
// Each kind maps to one DiffKind in pkg/orchestrator when desired and observed
// hashes disagree. A fingerprinter should only populate kinds whose desired
// bytes it can reproduce exactly; commands that need whole-service certainty
// must decide separately whether a partial fingerprint is acceptable.
type FileKind string

const (
	FileKindBinary FileKind = "binary" // executable artifact
	FileKindEnv    FileKind = "env"    // service env file
	FileKindUnit   FileKind = "unit"   // systemd unit
	FileKindCert   FileKind = "cert"   // TLS material (cert+key+ca)
)

// ExpectedFile is a per-kind desired-state record. Path is the absolute path
// on the host; SHA256 is the lowercase hex sha256 of the file's expected
// content as the role would write it. An empty SHA256 means the
// fingerprinter could identify the path but cannot assert the content hash.
type ExpectedFile struct {
	Path   string
	SHA256 string
}

// Fingerprint is the per-host desired-state snapshot a RoleFingerprinter
// produces. It is intentionally stateless — recomputed every run, never
// cached on disk or in the gitops repo. Files maps each modeled FileKind to
// the host path and expected sha256.
type Fingerprint struct {
	ServiceName string
	Host        string
	Files       map[FileKind]ExpectedFile
	ComputedAt  time.Time
}

// ProbeSHA256 returns sha256sum results for the given absolute paths on host
// via a single SSH invocation. The returned map is keyed by absolute path.
// Missing files are represented by an empty string — callers compare against
// the desired hash and decide whether absence is a diff.
func ProbeSHA256(ctx context.Context, pool *fwssh.Pool, host inventory.Host, paths []string) (map[string]string, error) {
	if pool == nil {
		return nil, fmt.Errorf("probe sha256: nil pool")
	}
	if len(paths) == 0 {
		return map[string]string{}, nil
	}

	cmd := BuildSHA256ProbeScript(paths)
	cfg := &fwssh.ConnectionConfig{
		Address:  host.ExternalIP,
		Port:     22,
		User:     host.User,
		HostName: host.Name,
		Timeout:  30 * time.Second,
	}
	result, err := pool.Run(ctx, cfg, cmd)
	if err != nil {
		return nil, fmt.Errorf("probe sha256 on %s: %w", host.Name, err)
	}
	if result == nil {
		return nil, fmt.Errorf("probe sha256 on %s: nil result", host.Name)
	}
	if result.ExitCode != 0 && strings.TrimSpace(result.Stdout) == "" {
		return nil, fmt.Errorf("probe sha256 on %s: exit=%d: %s", host.Name, result.ExitCode, strings.TrimSpace(result.Stderr))
	}
	return ParseSHA256ProbeOutput(paths, result.Stdout), nil
}

// BuildSHA256ProbeScript renders the shell script ProbeSHA256 runs remotely.
// It keeps the file path outside awk so shell-quoted paths cannot break awk's
// single-quoted program string.
func BuildSHA256ProbeScript(paths []string) string {
	var b strings.Builder
	for _, p := range paths {
		q := shellQuote(p)
		fmt.Fprintf(&b, "if [ -f %s ]; then hash=$(sha256sum %s 2>/dev/null | awk '{print $1}'); if [ -n \"$hash\" ]; then printf '%%s\\t%%s\\n' \"$hash\" %s; else printf 'MISSING\\t%%s\\n' %s; fi; else printf 'MISSING\\t%%s\\n' %s; fi\n",
			q, q, q, q, q)
	}
	return b.String()
}

// ParseSHA256ProbeOutput parses the "<sha>\t<path>" lines emitted by the
// remote probe script and fills in empty hashes for requested paths that did
// not produce a line.
func ParseSHA256ProbeOutput(paths []string, stdout string) map[string]string {
	out := make(map[string]string, len(paths))
	for line := range strings.SplitSeq(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 2)
		if len(fields) != 2 {
			continue
		}
		hash := strings.ToLower(strings.TrimSpace(fields[0]))
		path := strings.TrimSpace(fields[1])
		if hash == "missing" {
			out[path] = ""
			continue
		}
		out[path] = hash
	}
	// Make sure every requested path appears in the result, even if absent
	// from the script's output (defensive — keeps callers from null-deref).
	for _, p := range paths {
		if _, ok := out[p]; !ok {
			out[p] = ""
		}
	}
	return out
}

// SortedKinds returns the FileKinds in Files in stable order. Useful for
// deterministic output and tests.
func (f *Fingerprint) SortedKinds() []FileKind {
	if f == nil {
		return nil
	}
	out := make([]FileKind, 0, len(f.Files))
	for k := range f.Files {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return string(out[i]) < string(out[j]) })
	return out
}
