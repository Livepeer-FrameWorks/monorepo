package mistdiag

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultMistServiceName = "frameworks-mistserver"
	defaultMistInstallDir  = "/opt/frameworks/mistserver"
)

type CoreCollectOptions struct {
	Target  string
	KeyPath string
	Output  string
	Since   string
	Service string
}

type CoreAnalyzeOptions struct {
	BundlePath string
	CacheDir   string
}

type CoreAnalyzeResult struct {
	BundleDir     string
	CorePath      string
	BinaryPath    string
	DebugDir      string
	DebugArtifact string
	Debugger      string
	Backtrace     string
}

type mistArtifactManifest struct {
	Component             string `json:"component"`
	Version               string `json:"version"`
	ArtifactURL           string `json:"artifact_url"`
	ArtifactChecksum      string `json:"artifact_checksum"`
	DebugArtifactURL      string `json:"debug_artifact_url"`
	DebugArtifactChecksum string `json:"debug_artifact_checksum"`
	InstallDir            string `json:"install_dir"`
	ControllerBinary      string `json:"controller_binary"`
}

func CollectMistControllerCore(ctx context.Context, opts CoreCollectOptions) (string, error) {
	if strings.TrimSpace(opts.Target) == "" {
		return "", errors.New("ssh target is required")
	}
	if strings.TrimSpace(opts.Since) == "" {
		opts.Since = "2 hours ago"
	}
	if strings.TrimSpace(opts.Service) == "" {
		opts.Service = defaultMistServiceName
	}
	out := strings.TrimSpace(opts.Output)
	if out == "" {
		out = fmt.Sprintf("mistcontroller-core-%s-%s.tar.gz", safeBundleName(opts.Target), time.Now().UTC().Format("20060102T150405Z"))
	}

	script := buildCoreCollectScript(opts.Since, opts.Service)
	remotePath, err := runSSHScript(ctx, opts.Target, opts.KeyPath, script)
	if err != nil {
		return "", err
	}
	if err := scpFromTarget(ctx, opts.Target, opts.KeyPath, strings.TrimSpace(remotePath), out); err != nil {
		return "", err
	}
	return out, nil
}

func AnalyzeMistControllerCore(ctx context.Context, opts CoreAnalyzeOptions) (*CoreAnalyzeResult, error) {
	if strings.TrimSpace(opts.BundlePath) == "" {
		return nil, errors.New("bundle path is required")
	}
	workDir, err := os.MkdirTemp("", "frameworks-mist-core-*")
	if err != nil {
		return nil, fmt.Errorf("create analysis directory: %w", err)
	}
	if extractErr := extractTarGz(opts.BundlePath, workDir); extractErr != nil {
		return nil, extractErr
	}
	result := &CoreAnalyzeResult{BundleDir: workDir}

	result.BinaryPath = firstExistingPath(workDir,
		"MistController",
		"bin/MistController",
		"opt/frameworks/mistserver/bin/MistController",
	)
	if result.BinaryPath == "" {
		return result, fmt.Errorf("bundle does not contain MistController")
	}
	result.CorePath = firstGlob(workDir, "core*", "dump.core", "*.core")
	if result.CorePath == "" {
		return result, fmt.Errorf("bundle does not contain a core dump")
	}

	manifest, manifestErr := readMistArtifactManifest(filepath.Join(workDir, "manifest.json"))
	if manifestErr != nil && !errors.Is(manifestErr, os.ErrNotExist) {
		return result, manifestErr
	}
	if manifestErr == nil && manifest.DebugArtifactURL != "" {
		debugDir, artifact, debugErr := ensureDebugSymbols(ctx, manifest, opts.CacheDir)
		if debugErr != nil {
			return result, debugErr
		}
		result.DebugDir = debugDir
		result.DebugArtifact = artifact
	}

	debugger, backtrace, err := runCoreDebugger(ctx, result.BinaryPath, result.CorePath, result.DebugDir)
	result.Debugger = debugger
	result.Backtrace = backtrace
	if err != nil {
		return result, err
	}
	return result, nil
}

func buildCoreCollectScript(since, service string) string {
	serviceFile := safeBundleName(service)
	if serviceFile == "" {
		serviceFile = "mistserver"
	}
	return fmt.Sprintf(`set -eu
tmp="$(mktemp -d /tmp/frameworks-mist-core.XXXXXX)"
cp -f %[1]s/bin/MistController "$tmp/MistController" 2>/dev/null || true
cp -f %[1]s/manifest.json "$tmp/manifest.json" 2>/dev/null || true
cp -f /etc/mistserver.conf "$tmp/mistserver.conf" 2>/dev/null || true
uname -a > "$tmp/uname.txt" 2>&1 || true
coredumpctl info MistController > "$tmp/coredumpctl-info-MistController.txt" 2>&1 || true
coredumpctl dump MistController --output "$tmp/core.MistController" > "$tmp/coredumpctl-dump-MistController.txt" 2>&1 || true
journalctl --no-pager -u %[2]s --since %[3]s > "$tmp/journal-%[4]s.log" 2>&1 || true
tarball="/tmp/mistcontroller-core-$(hostname)-$(date -u +%%Y%%m%%dT%%H%%M%%SZ).tar.gz"
tar -czf "$tarball" -C "$tmp" .
printf '%%s\n' "$tarball"
`, defaultMistInstallDir, shellWord(service), shellWord(since), serviceFile)
}

func runSSHScript(ctx context.Context, target, keyPath, script string) (string, error) {
	args := sshArgs(target, keyPath)
	args = append(args, target, "sh", "-s")
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("collect core bundle over ssh: %w\n%s", err, string(out))
	}
	return strings.TrimSpace(string(out)), nil
}

func scpFromTarget(ctx context.Context, target, keyPath, remotePath, output string) error {
	if strings.TrimSpace(remotePath) == "" {
		return errors.New("remote collection did not return a tarball path")
	}
	args := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=15"}
	if keyPath != "" {
		args = append(args, "-i", keyPath)
	}
	args = append(args, target+":"+remotePath, output)
	cmd := exec.CommandContext(ctx, "scp", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("copy core bundle from edge: %w\n%s", err, string(out))
	}
	return nil
}

func sshArgs(_ string, keyPath string) []string {
	args := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=15"}
	if keyPath != "" {
		args = append(args, "-i", keyPath)
	}
	return args
}

func shellWord(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func safeBundleName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
			continue
		}
		b.WriteByte('-')
	}
	return strings.Trim(b.String(), "-")
}

func extractTarGz(path, dest string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open bundle: %w", err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("read gzip bundle: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read bundle tar: %w", err)
		}
		name := filepath.Clean(hdr.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return fmt.Errorf("bundle contains unsafe path %q", hdr.Name)
		}
		target := filepath.Join(dest, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, tr)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
}

func firstExistingPath(root string, rels ...string) string {
	for _, rel := range rels {
		path := filepath.Join(root, rel)
		if st, err := os.Stat(path); err == nil && !st.IsDir() {
			return path
		}
	}
	return ""
}

func firstGlob(root string, patterns ...string) string {
	var matches []string
	for _, pattern := range patterns {
		found, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			continue
		}
		matches = append(matches, found...)
	}
	sort.Strings(matches)
	for _, path := range matches {
		if st, err := os.Stat(path); err == nil && !st.IsDir() && st.Size() > 0 {
			return path
		}
	}
	return ""
}

func readMistArtifactManifest(path string) (mistArtifactManifest, error) {
	var manifest mistArtifactManifest
	data, err := os.ReadFile(path)
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func ensureDebugSymbols(ctx context.Context, manifest mistArtifactManifest, cacheDir string) (string, string, error) {
	if cacheDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", "", fmt.Errorf("resolve home directory: %w", err)
		}
		cacheDir = filepath.Join(home, ".cache", "frameworks", "mistserver-debug")
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", "", err
	}
	artifactName := filepath.Base(strings.Split(manifest.DebugArtifactURL, "?")[0])
	if artifactName == "." || artifactName == "/" || artifactName == "" {
		artifactName = "mistserver-debug.tar.gz"
	}
	artifactPath := filepath.Join(cacheDir, artifactName)
	if _, err := os.Stat(artifactPath); errors.Is(err, os.ErrNotExist) {
		if err := downloadFile(ctx, manifest.DebugArtifactURL, artifactPath); err != nil {
			return "", "", err
		}
	}
	if err := verifyOptionalChecksum(artifactPath, manifest.DebugArtifactChecksum); err != nil {
		return "", "", err
	}
	extractDir := filepath.Join(cacheDir, strings.TrimSuffix(strings.TrimSuffix(artifactName, ".gz"), ".tar"))
	if err := os.MkdirAll(extractDir, 0o755); err != nil {
		return "", "", err
	}
	if err := extractTarGz(artifactPath, extractDir); err != nil {
		return "", "", err
	}
	return extractDir, artifactPath, nil
}

func downloadFile(ctx context.Context, url, dest string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download debug symbols: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("download debug symbols: HTTP %d", resp.StatusCode)
	}
	tmp := dest + ".tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, resp.Body)
	closeErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, dest)
}

func verifyOptionalChecksum(path, checksum string) error {
	checksum = strings.TrimSpace(checksum)
	if checksum == "" {
		return nil
	}
	want := checksum
	if strings.HasPrefix(want, "sha256:") {
		want = strings.TrimPrefix(want, "sha256:")
	} else {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("debug artifact checksum mismatch: got sha256:%s, want sha256:%s", got, want)
	}
	return nil
}

func runCoreDebugger(ctx context.Context, binaryPath, corePath, debugDir string) (string, string, error) {
	if gdbPath, err := exec.LookPath("gdb"); err == nil {
		args := []string{"-batch", "-ex", "set debuginfod enabled off"}
		if debugDir != "" {
			args = append(args, "-ex", "set debug-file-directory "+debugSearchPath(debugDir))
		}
		args = append(args, "-ex", "file "+binaryPath, "-ex", "core-file "+corePath, "-ex", "thread apply all bt full")
		out, runErr := exec.CommandContext(ctx, gdbPath, args...).CombinedOutput()
		return "gdb", string(out), runErr
	}
	if lldbPath, err := exec.LookPath("lldb"); err == nil {
		args := []string{"--batch"}
		if debugDir != "" {
			args = append(args, "-o", "settings set target.debug-file-search-paths "+debugSearchPath(debugDir))
		}
		args = append(args, "-o", "target create "+binaryPath+" --core "+corePath, "-o", "thread backtrace all")
		out, runErr := exec.CommandContext(ctx, lldbPath, args...).CombinedOutput()
		return "lldb", string(out), runErr
	}
	return "", "", fmt.Errorf("no local debugger found; install gdb or lldb and retry")
}

func debugSearchPath(debugDir string) string {
	return strings.Join([]string{debugDir, filepath.Join(debugDir, "bin")}, string(os.PathListSeparator))
}
