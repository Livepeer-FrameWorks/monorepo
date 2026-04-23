package ansiblerun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const (
	// cacheSubdir is the per-user cache directory holding resolved Ansible
	// content. Keyed on the sha256 of requirements.yml so an unchanged
	// manifest skips both installs entirely.
	cacheSubdir = "frameworks/ansible"

	collectionsSubdir = "collections"
	rolesSubdir       = "roles"

	// sentinelFile records the requirements.yml hash that produced the
	// current cache contents. When its contents match the live hash, the
	// cache is fresh; otherwise it gets rebuilt.
	sentinelFile = ".requirements.sha256"

	// installLockFile serializes ansible-galaxy installs into the shared
	// per-user cache so parallel batch workers do not race extracting the
	// same role into the same destination.
	installLockFile = ".install.lock"
)

// EnsureResult carries both cache paths so the executor can propagate them
// as ANSIBLE_COLLECTIONS_PATH + ANSIBLE_ROLES_PATH.
type EnsureResult struct {
	CollectionsPath string
	RolesPath       string
}

// CollectionEnsurer resolves an Ansible requirements.yml into local cache
// directories, skipping work when the manifest hash matches the sentinel
// recorded from the last successful install.
//
// The cache root is shared across CLI invocations on the same machine;
// `ansible-galaxy collection install -p <dir>` and `ansible-galaxy role
// install --roles-path <dir>` are both idempotent given an unchanged
// requirements file, and our sentinel short-circuits even that.
type CollectionEnsurer struct {
	// RequirementsFile is the path to requirements.yml. Required.
	RequirementsFile string

	// CacheDir is the destination directory root. Default:
	// $XDG_CACHE_HOME/frameworks/ansible (or the OS equivalent via
	// os.UserCacheDir). Two subdirectories live under it: collections/ and
	// roles/. Callers may override for CI caches.
	CacheDir string

	// Binary overrides the ansible-galaxy binary; default "ansible-galaxy".
	Binary string
}

// Ensure resolves both collections and roles from requirements.yml into the
// cache. Returns the two paths for the caller to pass as
// ANSIBLE_COLLECTIONS_PATH + ANSIBLE_ROLES_PATH env vars.
func (e *CollectionEnsurer) Ensure(ctx context.Context) (EnsureResult, error) {
	if e.RequirementsFile == "" {
		return EnsureResult{}, errors.New("ansiblerun: RequirementsFile is required")
	}

	root, err := e.resolveCacheDir()
	if err != nil {
		return EnsureResult{}, err
	}
	collectionsPath := filepath.Join(root, collectionsSubdir)
	rolesPath := filepath.Join(root, rolesSubdir)

	for _, p := range []string{root, collectionsPath, rolesPath} {
		if mkErr := os.MkdirAll(p, 0o755); mkErr != nil {
			return EnsureResult{}, fmt.Errorf("create cache dir %s: %w", p, mkErr)
		}
	}

	hash, err := hashFile(e.RequirementsFile)
	if err != nil {
		return EnsureResult{}, err
	}

	fresh, sentinelErr := sentinelMatches(root, hash)
	if sentinelErr != nil {
		return EnsureResult{}, sentinelErr
	}
	if fresh {
		return EnsureResult{CollectionsPath: collectionsPath, RolesPath: rolesPath}, nil
	}

	lock, err := acquireInstallLock(root)
	if err != nil {
		return EnsureResult{}, err
	}
	defer releaseInstallLock(lock)

	// Another worker may have populated the cache while this caller was
	// waiting on the lock; re-check the sentinel before running galaxy.
	if fresh, err := sentinelMatches(root, hash); err != nil {
		return EnsureResult{}, err
	} else if fresh {
		return EnsureResult{CollectionsPath: collectionsPath, RolesPath: rolesPath}, nil
	}

	if err := runGalaxyCollectionInstall(ctx, e.galaxyBinary(), e.RequirementsFile, collectionsPath); err != nil {
		return EnsureResult{}, err
	}
	if err := runGalaxyRoleInstall(ctx, e.galaxyBinary(), e.RequirementsFile, rolesPath); err != nil {
		return EnsureResult{}, err
	}
	if err := writeSentinel(root, hash); err != nil {
		return EnsureResult{}, fmt.Errorf("write sentinel: %w", err)
	}
	return EnsureResult{CollectionsPath: collectionsPath, RolesPath: rolesPath}, nil
}

func (e *CollectionEnsurer) resolveCacheDir() (string, error) {
	if e.CacheDir != "" {
		abs, err := filepath.Abs(e.CacheDir)
		if err != nil {
			return "", fmt.Errorf("resolve CacheDir: %w", err)
		}
		return abs, nil
	}
	root, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate user cache dir: %w", err)
	}
	return filepath.Join(root, cacheSubdir), nil
}

func (e *CollectionEnsurer) galaxyBinary() string {
	if e.Binary != "" {
		return e.Binary
	}
	return "ansible-galaxy"
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sentinelMatches(cacheRoot, hash string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(cacheRoot, sentinelFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read sentinel: %w", err)
	}
	return string(data) == hash, nil
}

func writeSentinel(cacheRoot, hash string) error {
	return os.WriteFile(filepath.Join(cacheRoot, sentinelFile), []byte(hash), 0o644)
}

func acquireInstallLock(cacheRoot string) (*os.File, error) {
	lockPath := filepath.Join(cacheRoot, installLockFile)
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open install lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("lock install cache: %w", err)
	}
	return f, nil
}

func releaseInstallLock(f *os.File) {
	if f == nil {
		return
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		_ = err
	}
	_ = f.Close()
}

func runGalaxyCollectionInstall(ctx context.Context, binary, requirements, cacheDir string) error {
	return runGalaxyInstall(ctx, binary, requirements, cacheDir, "collection",
		[]string{"collection", "install", "-r"})
}

func runGalaxyRoleInstall(ctx context.Context, binary, requirements, cacheDir string) error {
	return runGalaxyInstall(ctx, binary, requirements, cacheDir, "role",
		[]string{"role", "install", "-r"})
}

// runGalaxyInstall dispatches the shared galaxy-install invocation. argv is
// the prefix up to (but not including) the requirements-file path; -p is
// appended for collection installs, --roles-path for role installs, resolved
// from `kind`.
func runGalaxyInstall(ctx context.Context, binary, requirements, cacheDir, kind string, argvPrefix []string) error {
	if _, err := exec.LookPath(binary); err != nil {
		return fmt.Errorf("%s not found on PATH: %w", binary, err)
	}
	reqDir := filepath.Dir(requirements)
	reqBase := filepath.Base(requirements)
	absCache, err := filepath.Abs(cacheDir)
	if err != nil {
		return fmt.Errorf("resolve cache dir: %w", err)
	}
	args := append([]string{}, argvPrefix...)
	args = append(args, reqBase)
	switch kind {
	case "collection":
		args = append(args, "-p", absCache)
	case "role":
		args = append(args, "--roles-path", absCache)
	default:
		return fmt.Errorf("unknown galaxy install kind %q", kind)
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = reqDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ansible-galaxy %s install failed: %w", kind, err)
	}
	return nil
}
