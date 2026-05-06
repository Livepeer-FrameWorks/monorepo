package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

type Result struct {
	Success     bool
	Detail      string
	RestartSelf bool
}

var componentInstallMu sync.Mutex

var artifactHTTPClient = &http.Client{Timeout: 30 * time.Minute}

func Apply(ctx context.Context, component *pb.DesiredComponent) Result {
	if component == nil {
		return Result{Detail: "empty component"}
	}
	name := strings.TrimSpace(component.GetComponent())
	version := strings.TrimSpace(component.GetVersion())
	if name == "" {
		return Result{Detail: "component name required"}
	}
	if name == "config_schema" {
		componentInstallMu.Lock()
		defer componentInstallMu.Unlock()
		if err := WriteComponentVersion(name, version); err != nil {
			return Result{Detail: err.Error()}
		}
		return Result{Success: true, Detail: "version recorded"}
	}
	if strings.TrimSpace(os.Getenv("DEPLOY_MODE")) == "docker" {
		return Result{Detail: "docker-mode component updates require a container image rollout"}
	}
	if strings.TrimSpace(component.GetArtifactUrl()) == "" {
		return Result{Detail: "artifact_url required"}
	}
	if strings.TrimSpace(component.GetChecksum()) == "" {
		return Result{Detail: "checksum required"}
	}

	artifact, cleanup, err := downloadArtifact(ctx, component)
	if err != nil {
		return Result{Detail: err.Error()}
	}
	defer cleanup()

	componentInstallMu.Lock()
	defer componentInstallMu.Unlock()

	var restartSelf bool
	switch name {
	case "helmsman":
		restartSelf, err = applyHelmsman(artifact)
	case "mist":
		err = applyMistServer(ctx, artifact)
	case "caddy":
		err = applyCaddy(ctx, artifact)
	default:
		err = fmt.Errorf("unsupported component %q", name)
	}
	if err != nil {
		return Result{Detail: err.Error()}
	}
	if err := WriteComponentVersion(name, version); err != nil {
		return Result{Detail: err.Error()}
	}
	return Result{Success: true, Detail: "artifact installed", RestartSelf: restartSelf}
}

func WriteComponentVersion(component, version string) error {
	key, err := componentVersionKey(component)
	if err != nil {
		return err
	}
	version = strings.TrimSpace(version)
	if version == "" {
		return fmt.Errorf("%s version required", strings.TrimSpace(component))
	}
	if !envLineValueSafe(version) {
		return fmt.Errorf("%s version contains unsupported control characters", strings.TrimSpace(component))
	}
	path := componentVersionPath()
	if path == "" {
		return fmt.Errorf("component version path unavailable")
	}
	values := map[string]string{}
	if b, readErr := os.ReadFile(path); readErr == nil {
		for line := range strings.SplitSeq(string(b), "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if ok && k != "" {
				values[k] = v
			}
		}
	}
	values[key] = version
	if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr != nil {
		return mkdirErr
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	for _, k := range sortedKeys(values) {
		if _, err := fmt.Fprintf(f, "%s=%s\n", k, values[k]); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func componentVersionKey(component string) (string, error) {
	switch strings.TrimSpace(component) {
	case "helmsman":
		return "HELMSMAN_VERSION", nil
	case "mist":
		return "MIST_VERSION", nil
	case "caddy":
		return "CADDY_VERSION", nil
	case "config_schema":
		return "CONFIG_SCHEMA_VERSION", nil
	default:
		return "", fmt.Errorf("unsupported component %q", strings.TrimSpace(component))
	}
}

func envLineValueSafe(value string) bool {
	return !strings.ContainsAny(value, "\r\n\x00")
}

func ReadComponentVersions() map[string]string {
	out := map[string]string{}
	if path := componentVersionPath(); path != "" {
		if b, err := os.ReadFile(path); err == nil {
			for line := range strings.SplitSeq(string(b), "\n") {
				k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
				if ok && k != "" {
					out[k] = v
				}
			}
		}
	}
	return out
}

func componentVersionPath() string {
	for _, p := range []string{
		"/etc/frameworks/component-versions.env",
		"/usr/local/etc/frameworks/component-versions.env",
	} {
		if _, err := os.Stat(filepath.Dir(p)); err == nil {
			return p
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config/frameworks/component-versions.env")
	}
	return ""
}

func downloadArtifact(ctx context.Context, component *pb.DesiredComponent) (string, func(), error) {
	dir, err := os.MkdirTemp("", "frameworks-update-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, component.GetArtifactUrl(), nil)
	if err != nil {
		cleanup()
		return "", cleanup, fmt.Errorf("prepare artifact download: %w", err)
	}
	resp, err := artifactHTTPClient.Do(req)
	if err != nil {
		cleanup()
		return "", cleanup, fmt.Errorf("download artifact: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		cleanup()
		return "", cleanup, fmt.Errorf("download artifact: HTTP %d", resp.StatusCode)
	}
	path := filepath.Join(dir, "artifact")
	file, err := os.Create(path)
	if err != nil {
		cleanup()
		return "", cleanup, err
	}
	hasher, expected, err := checksum(component.GetChecksum())
	if err != nil {
		_ = file.Close()
		cleanup()
		return "", cleanup, err
	}
	writer := io.Writer(file)
	if hasher != nil {
		writer = io.MultiWriter(file, hasher)
	}
	if _, err := io.Copy(writer, resp.Body); err != nil {
		_ = file.Close()
		cleanup()
		return "", cleanup, fmt.Errorf("write artifact: %w", err)
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", cleanup, err
	}
	if hasher != nil && !strings.EqualFold(expected, hex.EncodeToString(hasher.Sum(nil))) {
		cleanup()
		return "", cleanup, fmt.Errorf("verify artifact checksum: got %s", hex.EncodeToString(hasher.Sum(nil)))
	}
	return path, cleanup, nil
}

func checksum(value string) (hash.Hash, string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, "", fmt.Errorf("checksum required")
	}
	algo, expected, ok := strings.Cut(value, ":")
	if !ok {
		algo, expected = "sha256", value
	}
	switch strings.ToLower(algo) {
	case "sha256":
		if err := validateChecksumDigest(expected, sha256.Size*2); err != nil {
			return nil, "", err
		}
		return sha256.New(), expected, nil
	case "sha512":
		if err := validateChecksumDigest(expected, sha512.Size*2); err != nil {
			return nil, "", err
		}
		return sha512.New(), expected, nil
	default:
		return nil, "", fmt.Errorf("unsupported checksum algorithm %q", algo)
	}
}

func validateChecksumDigest(expected string, hexLen int) error {
	expected = strings.TrimSpace(expected)
	if len(expected) != hexLen {
		return fmt.Errorf("checksum digest must be %d hex characters", hexLen)
	}
	if _, err := hex.DecodeString(expected); err != nil {
		return fmt.Errorf("checksum digest must be hex: %w", err)
	}
	return nil
}

func applyHelmsman(artifact string) (bool, error) {
	exe, err := os.Executable()
	if err != nil {
		return false, err
	}
	binary, cleanup, err := executableFromArtifact(artifact, []string{"helmsman", "frameworks"}, "frameworks-helmsman-")
	if err != nil {
		return false, err
	}
	defer cleanup()
	if err := installFile(binary, exe, 0o755); err != nil {
		return false, err
	}
	return true, nil
}

func applyMistServer(ctx context.Context, artifact string) error {
	root := componentInstallDir("mistserver")
	staging, err := extractArtifactSibling(root, artifact)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if _, statErr := os.Stat(filepath.Join(staging, "bin", "MistController")); statErr != nil {
		return fmt.Errorf("MistController missing from artifact")
	}
	replacement, err := mistPayloadReplacement(staging, root)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(replacement.src) }()
	return replaceDirsAtomically([]dirReplacement{replacement}, func() error {
		return signalMistController(ctx)
	})
}

func mistPayloadReplacement(staging, root string) (dirReplacement, error) {
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return dirReplacement{}, err
	}
	replacementRoot, err := os.MkdirTemp(parent, ".mistserver-root-*")
	if err != nil {
		return dirReplacement{}, err
	}
	cleanup := func(err error) (dirReplacement, error) {
		_ = os.RemoveAll(replacementRoot)
		return dirReplacement{}, err
	}
	if info, statErr := os.Stat(root); statErr == nil {
		if !info.IsDir() {
			return cleanup(fmt.Errorf("MistServer install root is not a directory: %s", root))
		}
		if copyErr := copyDirTree(root, replacementRoot); copyErr != nil {
			return cleanup(copyErr)
		}
	} else if errors.Is(statErr, os.ErrNotExist) {
		if mkErr := os.MkdirAll(replacementRoot, 0o755); mkErr != nil {
			return cleanup(mkErr)
		}
	} else {
		return cleanup(statErr)
	}

	payloadDirs := []string{"bin", "lib"}
	replaced := false
	for _, dir := range payloadDirs {
		src := filepath.Join(staging, dir)
		info, err := os.Stat(src)
		if errors.Is(err, os.ErrNotExist) {
			if dir == "lib" {
				if _, oldErr := os.Stat(filepath.Join(root, dir)); oldErr == nil {
					return cleanup(fmt.Errorf("MistServer artifact missing %s directory required by current install", dir))
				} else if !errors.Is(oldErr, os.ErrNotExist) {
					return cleanup(oldErr)
				}
			}
			continue
		}
		if err != nil {
			return cleanup(err)
		}
		if !info.IsDir() {
			return cleanup(fmt.Errorf("MistServer artifact %s is not a directory", dir))
		}
		dst := filepath.Join(replacementRoot, dir)
		if err := os.RemoveAll(dst); err != nil {
			return cleanup(err)
		}
		if err := os.Rename(src, dst); err != nil {
			return cleanup(err)
		}
		replaced = true
	}
	if !replaced {
		return cleanup(fmt.Errorf("MistServer artifact has no payload directories"))
	}
	if _, err := os.Stat(filepath.Join(replacementRoot, "bin", "MistController")); err != nil {
		return cleanup(fmt.Errorf("MistController missing from staged install root"))
	}
	return dirReplacement{src: replacementRoot, dst: root}, nil
}

func copyDirTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		mode := info.Mode()
		switch {
		case d.IsDir():
			return os.MkdirAll(target, mode.Perm())
		case mode.Type() == os.ModeSymlink:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		case mode.IsRegular():
			return copyFile(path, target, mode.Perm())
		default:
			return nil
		}
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

func applyCaddy(ctx context.Context, artifact string) error {
	root := componentInstallDir("caddy")
	binary, cleanup, err := executableFromArtifact(artifact, []string{"caddy"}, "")
	if err != nil {
		return err
	}
	defer cleanup()
	if err := installFile(binary, filepath.Join(root, "caddy"), 0o755); err != nil {
		return err
	}
	return restartCaddy(ctx)
}

func componentInstallDir(component string) string {
	if runtime.GOOS == "darwin" {
		if home, err := os.UserHomeDir(); err == nil {
			userPath := filepath.Join(home, ".local/opt/frameworks", component)
			if _, statErr := os.Stat(userPath); statErr == nil {
				return userPath
			}
		}
		return filepath.Join("/usr/local/opt/frameworks", component)
	}
	return filepath.Join("/opt/frameworks", component)
}

func executableFromArtifact(artifact string, names []string, prefix string) (string, func(), error) {
	staging, err := os.MkdirTemp("", "frameworks-extract-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(staging) }
	extractErr := extractInto(artifact, staging)
	if extractErr != nil {
		cleanup()
		return "", cleanup, extractErr
	}
	var found string
	err = filepath.WalkDir(staging, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || found != "" {
			return err
		}
		base := filepath.Base(path)
		for _, name := range names {
			if base == name || (prefix != "" && strings.HasPrefix(base, prefix)) {
				found = path
				return nil
			}
		}
		return nil
	})
	if err != nil {
		cleanup()
		return "", cleanup, err
	}
	if found == "" {
		var files []string
		walkErr := filepath.WalkDir(staging, func(path string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				files = append(files, path)
			}
			return nil
		})
		if walkErr != nil {
			cleanup()
			return "", cleanup, walkErr
		}
		if len(files) == 1 {
			found = files[0]
		}
	}
	if found == "" {
		cleanup()
		return "", cleanup, fmt.Errorf("component binary not found in artifact")
	}
	return found, cleanup, nil
}

func extractArtifactSibling(root, artifact string) (string, error) {
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", err
	}
	staging, err := os.MkdirTemp(parent, ".mistserver-update-*")
	if err != nil {
		return "", err
	}
	if err := extractInto(artifact, staging); err != nil {
		_ = os.RemoveAll(staging)
		return "", err
	}
	return staging, nil
}

func extractInto(artifact, dest string) error {
	if err := extractTarGz(artifact, dest); err == nil {
		return nil
	}
	if err := extractZip(artifact, dest); err == nil {
		return nil
	}
	return installFile(artifact, filepath.Join(dest, filepath.Base(artifact)), 0o755)
}

func extractTarGz(artifact, dest string) error {
	file, err := os.Open(artifact)
	if err != nil {
		return err
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
}

func extractZip(artifact, dest string) error {
	zr, err := zip.OpenReader(artifact)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		target, err := safeJoin(dest, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			mkdirErr := os.MkdirAll(target, 0o755)
			if mkdirErr != nil {
				return mkdirErr
			}
			continue
		}
		mkdirErr := os.MkdirAll(filepath.Dir(target), 0o755)
		if mkdirErr != nil {
			return mkdirErr
		}
		src, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, f.Mode()|0o755)
		if err != nil {
			_ = src.Close()
			return err
		}
		_, copyErr := io.Copy(out, src)
		closeErr := errors.Join(src.Close(), out.Close())
		if copyErr != nil || closeErr != nil {
			return errors.Join(copyErr, closeErr)
		}
	}
	return nil
}

func safeJoin(root, name string) (string, error) {
	target := filepath.Clean(filepath.Join(root, name))
	root = filepath.Clean(root)
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("artifact path escapes destination: %s", name)
	}
	return target, nil
}

func installFile(src, dst string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".new"
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

type dirReplacement struct {
	src       string
	dst       string
	hadDst    bool
	installed bool
}

func replaceDirsAtomically(replacements []dirReplacement, after func() error) error {
	prepared := make([]dirReplacement, len(replacements))
	copy(prepared, replacements)
	for i := range prepared {
		_, statErr := os.Stat(prepared[i].dst)
		if statErr == nil {
			if exchErr := exchangeDirs(prepared[i].src, prepared[i].dst); exchErr != nil {
				rollbackErr := rollbackDirReplacements(prepared)
				return errors.Join(exchErr, rollbackErr)
			}
			prepared[i].hadDst = true
			prepared[i].installed = true
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return errors.Join(statErr, rollbackDirReplacements(prepared))
		}
		if renameErr := os.Rename(prepared[i].src, prepared[i].dst); renameErr != nil {
			rollbackErr := rollbackDirReplacements(prepared)
			return errors.Join(renameErr, rollbackErr)
		}
		prepared[i].installed = true
	}
	if after != nil {
		if err := after(); err != nil {
			rollbackErr := rollbackDirReplacements(prepared)
			return errors.Join(err, rollbackErr)
		}
	}
	for i := range prepared {
		if prepared[i].hadDst {
			if err := os.RemoveAll(prepared[i].src); err != nil {
				return err
			}
		}
	}
	return nil
}

func rollbackDirReplacements(replacements []dirReplacement) error {
	var errs []error
	for i := len(replacements) - 1; i >= 0; i-- {
		replacement := replacements[i]
		if replacement.installed {
			if replacement.hadDst {
				if err := exchangeDirs(replacement.src, replacement.dst); err != nil {
					errs = append(errs, fmt.Errorf("restore previous install %s: %w", replacement.dst, err))
				}
				continue
			}
			if err := os.RemoveAll(replacement.dst); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func signalMistController(ctx context.Context) error {
	if runtime.GOOS == "darwin" {
		errSystem := runCommand(ctx, "launchctl", "kill", "USR1", "system/com.livepeer.frameworks.mistserver")
		if errSystem == nil {
			return nil
		}
		errUser := runCommand(ctx, "launchctl", "kill", "USR1", fmt.Sprintf("gui/%d/com.livepeer.frameworks.mistserver", os.Getuid()))
		if errUser == nil {
			return nil
		}
		errProc := runCommand(ctx, "pkill", "-USR1", "-f", "MistController")
		if errProc == nil {
			return nil
		}
		return errors.Join(errSystem, errUser, errProc)
	}
	command, args := linuxMistControllerSignalCommand()
	errService := runCommand(ctx, command, args...)
	if errService == nil {
		return nil
	}
	errProc := runCommand(ctx, "pkill", "-USR1", "-f", "MistController")
	if errProc == nil {
		return nil
	}
	return errors.Join(errService, errProc)
}

func linuxMistControllerSignalCommand() (string, []string) {
	return "systemctl", []string{"kill", "--kill-whom=main", "-s", "USR1", "frameworks-mistserver"}
}

func restartCaddy(ctx context.Context) error {
	if runtime.GOOS == "darwin" {
		return firstCommand(ctx,
			"launchctl", "kickstart", "-k", "system/com.livepeer.frameworks.caddy",
			"launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/com.livepeer.frameworks.caddy", os.Getuid()),
		)
	}
	return runCommand(ctx, "systemctl", "restart", "frameworks-caddy")
}

func firstCommand(ctx context.Context, first ...string) error {
	if len(first)%4 != 0 {
		return fmt.Errorf("invalid command list")
	}
	var errs []error
	for i := 0; i < len(first); i += 4 {
		if err := runCommand(ctx, first[i], first[i+1:i+4]...); err == nil {
			return nil
		} else {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func runCommand(ctx context.Context, name string, args ...string) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
