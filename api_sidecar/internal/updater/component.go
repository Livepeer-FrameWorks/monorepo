package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"frameworks/api_sidecar/internal/appconfig"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"hash"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

type Result struct {
	Success     bool
	Detail      string
	RestartSelf bool
}

var componentInstallMu sync.Mutex

var artifactHTTPClient = &http.Client{Timeout: 30 * time.Minute}

func Apply(ctx context.Context, component *ipcpb.DesiredComponent) Result {
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
	// A containerized helmsman WITHOUT s6 supervision is the retired
	// multi-container layout: Mist and Caddy run in other containers, so an
	// in-place swap here would target processes that don't exist. Refuse
	// with an actionable message; those nodes migrate by re-provisioning
	// onto the single edge image.
	if deployMode := strings.TrimSpace(appconfig.Runtime().DeployMode); deployMode != "" && deployMode != "native" && !s6SupervisionPresent() {
		return Result{Detail: "containerized helmsman without s6 supervision (retired multi-container layout); re-provision with the single edge image to enable in-place updates"}
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
	detail := ""
	switch name {
	case "helmsman":
		restartSelf, err = applyHelmsman(artifact, component)
	case "mist":
		detail, err = applyMistServer(ctx, artifact, component)
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
	if detail == "" {
		detail = "artifact installed"
	}
	return Result{Success: true, Detail: detail, RestartSelf: restartSelf}
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

// ComponentVersionKey exposes the component-versions.env key for a component
// so the edge-container seed can compare image-baked versions against
// installed ones.
func ComponentVersionKey(component string) (string, error) {
	return componentVersionKey(component)
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

func downloadArtifact(ctx context.Context, component *ipcpb.DesiredComponent) (string, func(), error) {
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

func applyHelmsman(artifact string, component *ipcpb.DesiredComponent) (bool, error) {
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
	if err := writeComponentInstallSentinel(filepath.Dir(exe), component); err != nil {
		return false, err
	}
	return true, nil
}

func applyMistServer(ctx context.Context, artifact string, component *ipcpb.DesiredComponent) (string, error) {
	root := componentInstallDir("mistserver")
	staging, err := extractArtifactSibling(root, artifact)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	if _, statErr := os.Stat(filepath.Join(staging, "bin", "MistController")); statErr != nil {
		return "", fmt.Errorf("MistController missing from artifact")
	}
	replacement, err := mistPayloadReplacement(staging, root)
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(replacement.src) }()
	if err := writeMistManagedMetadata(replacement.src, component); err != nil {
		return "", err
	}
	if err := replaceMistPayloadInPlace(replacement.src, replacement.dst, func() error {
		return currentServiceController().SignalMistUSR1(ctx)
	}); err != nil {
		return "", err
	}
	// Signal delivery is the commit point: once Mist begins its handoff, the
	// new payload must remain available for delayed controller/listener execs,
	// so a failed reload is recovered forward by a restart, never by rolling
	// files back.
	return completeMistReload(ctx, filepath.Join(root, "bin", "MistController"))
}

// verifyMistReload is replaced in tests; production waits for the controller
// to run the new executable and for its API to answer an authenticated call.
var verifyMistReload = func(ctx context.Context, controllerPath string) error {
	if err := waitMistControllerReload(ctx, controllerPath); err != nil {
		return err
	}
	return waitMistAPIReady(ctx)
}

// completeMistReload restarts MistServer when the rolling reload did not
// produce a controller that serves its API. Without this a controller wedged
// in the handoff keeps the node alive for Foghorn while every Mist API call
// fails. The restart drops active sessions, so the returned detail says so.
func completeMistReload(ctx context.Context, controllerPath string) (string, error) {
	reloadErr := verifyMistReload(ctx, controllerPath)
	if reloadErr == nil {
		return "", nil
	}
	if err := currentServiceController().RestartMist(ctx); err != nil {
		return "", fmt.Errorf("MistServer rolling reload failed (%w) and restart failed: %w", reloadErr, err)
	}
	if err := verifyMistReload(ctx, controllerPath); err != nil {
		return "", fmt.Errorf("MistServer rolling reload failed (%w) and it is still unavailable after a restart: %w", reloadErr, err)
	}
	return fmt.Sprintf("artifact installed; MistServer restarted because the rolling reload failed: %v", reloadErr), nil
}

func writeMistManagedMetadata(root string, component *ipcpb.DesiredComponent) error {
	if err := writeComponentInstallSentinel(root, component); err != nil {
		return err
	}
	manifest := struct {
		Component        string `json:"component"`
		Version          string `json:"version"`
		ArtifactURL      string `json:"artifact_url"`
		ArtifactChecksum string `json:"artifact_checksum"`
		ONNXProfile      string `json:"onnx_profile"`
		InstallDir       string `json:"install_dir"`
		ControllerBinary string `json:"controller_binary"`
	}{
		Component:        "mistserver",
		Version:          strings.TrimSpace(component.GetVersion()),
		ArtifactURL:      strings.TrimSpace(component.GetArtifactUrl()),
		ArtifactChecksum: strings.TrimSpace(component.GetChecksum()),
		ONNXProfile:      strings.TrimSpace(component.GetOnnxProfile()),
		InstallDir:       root,
		ControllerBinary: filepath.Join(root, "bin", "MistController"),
	}
	payload, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	path := filepath.Join(root, "manifest.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeComponentInstallSentinel(root string, component *ipcpb.DesiredComponent) error {
	if component == nil {
		return nil
	}
	identity := strings.TrimSpace(component.GetChecksum())
	if identity == "" {
		identity = strings.TrimSpace(component.GetArtifactUrl())
	}
	if identity == "" {
		return nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	path := componentInstallSentinelPath(root, identity)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return file.Close()
}

func componentInstallSentinelPath(root, identity string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(identity)))
	return filepath.Join(root, ".installed-"+hex.EncodeToString(sum[:]))
}

type mistPayloadStage struct {
	src string
	dst string
}

func mistPayloadReplacement(staging, root string) (mistPayloadStage, error) {
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return mistPayloadStage{}, err
	}
	replacementRoot, err := os.MkdirTemp(parent, ".mistserver-root-*")
	if err != nil {
		return mistPayloadStage{}, err
	}
	cleanup := func(err error) (mistPayloadStage, error) {
		_ = os.RemoveAll(replacementRoot)
		return mistPayloadStage{}, err
	}
	if info, statErr := os.Stat(root); statErr == nil && !info.IsDir() {
		return cleanup(fmt.Errorf("MistServer install root is not a directory: %s", root))
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return cleanup(statErr)
	}

	// Stage only the release-owned payload. Unmanaged files in the live root
	// remain untouched when this tree is synchronized into the stable install
	// directories. Optional directories absent from the artifact are removed.
	payloadDirs := []string{"bin", "lib", "share", "opt"}
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
	return mistPayloadStage{src: replacementRoot, dst: root}, nil
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
	return currentServiceController().RestartCaddy(ctx)
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
		case tar.TypeSymlink:
			if filepath.IsAbs(hdr.Linkname) {
				return fmt.Errorf("archive symlink target must be relative: %s", hdr.Linkname)
			}
			if _, err := safeJoin(dest, filepath.Join(filepath.Dir(hdr.Name), hdr.Linkname)); err != nil {
				return fmt.Errorf("archive symlink target escapes destination: %s -> %s", hdr.Name, hdr.Linkname)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.RemoveAll(target); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
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

var mistPayloadDirs = []string{"lib", "share", "opt", "bin"}

// replaceMistPayloadInPlace keeps the canonical Mist directory names stable.
// Mist's rolling restart resolves its replacement binary from /proc/self/exe;
// renaming the install root would make that path follow the old tree. Files are
// instead replaced with rename(2), which leaves running mappings alive while
// the canonical bin directory remains the location used by every later exec.
func replaceMistPayloadInPlace(stagedRoot, root string, after func() error) error {
	parent := filepath.Dir(root)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return err
	}
	backup, err := os.MkdirTemp(parent, ".mistserver-backup-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(backup) }()

	rootExisted := false
	if info, statErr := os.Stat(root); statErr == nil {
		if !info.IsDir() {
			return fmt.Errorf("MistServer install root is not a directory: %s", root)
		}
		rootExisted = true
		if err := copyDirTree(root, backup); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}

	rollback := func(cause error) error {
		if !rootExisted {
			return errors.Join(cause, os.RemoveAll(root))
		}
		return errors.Join(cause, syncMistManagedTree(backup, root))
	}
	if err := syncMistManagedTree(stagedRoot, root); err != nil {
		return rollback(err)
	}
	if after != nil {
		if err := after(); err != nil {
			return rollback(err)
		}
	}
	return nil
}

func syncMistManagedTree(srcRoot, dstRoot string) error {
	if err := os.MkdirAll(dstRoot, 0o755); err != nil {
		return err
	}
	// Libraries and support data land before binaries. MistController is
	// installed last within bin/, so it cannot restart against a partial tree.
	for _, dir := range mistPayloadDirs {
		if err := syncDirectoryInPlace(filepath.Join(srcRoot, dir), filepath.Join(dstRoot, dir), dir == "bin"); err != nil {
			return fmt.Errorf("sync MistServer %s: %w", dir, err)
		}
	}
	if err := syncMistMetadata(srcRoot, dstRoot); err != nil {
		return fmt.Errorf("sync MistServer metadata: %w", err)
	}
	return nil
}

type treeEntry struct {
	rel  string
	info os.FileInfo
}

func syncDirectoryInPlace(src, dst string, controllerLast bool) error {
	srcInfo, err := os.Lstat(src)
	if errors.Is(err, os.ErrNotExist) {
		return os.RemoveAll(dst)
	}
	if err != nil {
		return err
	}
	if !srcInfo.IsDir() {
		return fmt.Errorf("source is not a directory: %s", src)
	}
	if dstInfo, statErr := os.Lstat(dst); statErr == nil && !dstInfo.IsDir() {
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err := os.MkdirAll(dst, srcInfo.Mode().Perm()); err != nil {
		return err
	}

	var dirs, files []treeEntry
	present := map[string]struct{}{filepath.Clean("."): {}}
	if err := filepath.WalkDir(src, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		present[rel] = struct{}{}
		if rel == "." {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		entry := treeEntry{rel: rel, info: info}
		if d.IsDir() {
			dirs = append(dirs, entry)
		} else {
			files = append(files, entry)
		}
		return nil
	}); err != nil {
		return err
	}
	for _, entry := range dirs {
		target := filepath.Join(dst, entry.rel)
		if info, statErr := os.Lstat(target); statErr == nil && !info.IsDir() {
			if err := os.RemoveAll(target); err != nil {
				return err
			}
		}
		if err := os.MkdirAll(target, entry.info.Mode().Perm()); err != nil {
			return err
		}
	}
	if controllerLast {
		slices.SortStableFunc(files, func(a, b treeEntry) int {
			aController := a.rel == "MistController"
			bController := b.rel == "MistController"
			if aController != bController {
				if aController {
					return 1
				}
				return -1
			}
			return strings.Compare(a.rel, b.rel)
		})
	}
	for _, entry := range files {
		if err := installTreeEntry(filepath.Join(src, entry.rel), filepath.Join(dst, entry.rel), entry.info); err != nil {
			return err
		}
	}

	var stale []string
	if err := filepath.WalkDir(dst, func(path string, _ os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, relErr := filepath.Rel(dst, path)
		if relErr != nil {
			return relErr
		}
		if _, ok := present[rel]; !ok {
			stale = append(stale, path)
		}
		return nil
	}); err != nil {
		return err
	}
	slices.SortFunc(stale, func(a, b string) int { return len(b) - len(a) })
	for _, path := range stale {
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return nil
}

func installTreeEntry(src, dst string, info os.FileInfo) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".mist-entry-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Remove(tmpPath); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmpPath) }()

	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.Symlink(target, tmpPath); err != nil {
			return err
		}
	case info.Mode().IsRegular():
		if err := copyFile(src, tmpPath, info.Mode().Perm()); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported payload entry %s (%s)", src, info.Mode())
	}
	if dstInfo, statErr := os.Lstat(dst); statErr == nil && dstInfo.IsDir() {
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	return os.Rename(tmpPath, dst)
}

func syncMistMetadata(srcRoot, dstRoot string) error {
	entries, readErr := os.ReadDir(srcRoot)
	if readErr != nil {
		return readErr
	}
	wantedSentinels := map[string]struct{}{}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".installed-") {
			wantedSentinels[entry.Name()] = struct{}{}
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			if installErr := installTreeEntry(filepath.Join(srcRoot, entry.Name()), filepath.Join(dstRoot, entry.Name()), info); installErr != nil {
				return installErr
			}
		}
	}
	if info, statErr := os.Lstat(filepath.Join(srcRoot, "manifest.json")); statErr == nil {
		if installErr := installTreeEntry(filepath.Join(srcRoot, "manifest.json"), filepath.Join(dstRoot, "manifest.json"), info); installErr != nil {
			return installErr
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	} else if removeErr := os.Remove(filepath.Join(dstRoot, "manifest.json")); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		return removeErr
	}
	dstEntries, dstReadErr := os.ReadDir(dstRoot)
	if dstReadErr != nil {
		return dstReadErr
	}
	for _, entry := range dstEntries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".installed-") {
			if _, ok := wantedSentinels[entry.Name()]; !ok {
				if err := os.Remove(filepath.Join(dstRoot, entry.Name())); err != nil {
					return err
				}
			}
		}
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
