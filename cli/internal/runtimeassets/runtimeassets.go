// Package runtimeassets ships the on-disk content the CLI needs at runtime
// (the frameworks.infra Ansible collection, its playbooks and ansible.cfg, and
// the Skipper sitemap/FAQ sources) inside the binary, so a released CLI works
// from any directory without a monorepo checkout.
//
// The cli module cannot //go:embed paths above cli/, so
// scripts/cli-embed-assets.sh stages them into bundle/ before every shipping
// build (make build-bin-cli, make test-cli, release.yml). bundle/ is
// gitignored except for its .gitignore, which keeps the embed pattern valid in
// a fresh clone: go build, go vet and go test compile without staging, and a
// binary built that way reports the missing bundle at the point of use.
package runtimeassets

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
)

//go:embed all:bundle
var bundleFS embed.FS

// AnsibleRootEnv points the CLI at an ansible/ directory on disk, bypassing
// the embedded copy. Used by CI and role development.
const AnsibleRootEnv = "FRAMEWORKS_ANSIBLE_ROOT"

// Release tags are vX.Y.Z with at most one prerelease segment (v0.3.11-rc3).
// git-describe output past a tag (v0.3.11-4-gabc123), any -dirty tree and the
// "dev" default are development builds.
var releaseVersionPattern = regexp.MustCompile(`^v\d+\.\d+\.\d+(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?$`)

// resolver carries every input the resolution reads so tests can substitute
// the embedded tree, cache root, working directory and build version.
type resolver struct {
	ansible   fs.FS
	skipper   fs.FS
	version   string
	cacheRoot func() (string, error)
	getwd     func() (string, error)
	getenv    func(string) string

	hashOnce sync.Once
	hash     string
	hashErr  error
}

var (
	defaultOnce     sync.Once
	defaultResolver *resolver
)

func defaultRes() *resolver {
	defaultOnce.Do(func() {
		defaultResolver = &resolver{
			ansible:   subOrNil("bundle/ansible"),
			skipper:   subOrNil("bundle/skipper"),
			version:   version.Version,
			cacheRoot: os.UserCacheDir,
			getwd:     os.Getwd,
			getenv:    os.Getenv,
		}
	})
	return defaultResolver
}

// subOrNil returns nil when dir is not a valid path, which the resolution
// reports as "no embedded content".
func subOrNil(dir string) fs.FS {
	sub, err := fs.Sub(bundleFS, dir)
	if err != nil {
		return nil
	}
	return sub
}

// AnsibleRoot returns a directory containing ansible.cfg, requirements.yml,
// playbooks/ and collections/ansible_collections/frameworks.
//
// Order: $FRAMEWORKS_ANSIBLE_ROOT; then, for development builds only, an
// ansible/ directory above the working directory (so role edits apply without
// a rebuild); then the embedded tree extracted to the user cache. Release
// builds never read the working directory: the roles they run are the ones
// they were built with.
func AnsibleRoot() (string, error) { return defaultRes().ansibleRoot() }

// SkipperSources returns the Skipper sitemaps/ and faq/ tree. Development
// builds prefer config/skipper above the working directory; release builds
// read only the embedded copy.
func SkipperSources() (fs.FS, error) { return defaultRes().skipperSources() }

func (r *resolver) isRelease() bool {
	return releaseVersionPattern.MatchString(r.version) && !strings.HasSuffix(r.version, "-dirty")
}

func (r *resolver) ansibleRoot() (string, error) {
	if override := r.getenv(AnsibleRootEnv); override != "" {
		abs, err := filepath.Abs(override)
		if err != nil {
			return "", fmt.Errorf("resolve %s: %w", AnsibleRootEnv, err)
		}
		if _, err := os.Stat(filepath.Join(abs, "ansible.cfg")); err != nil {
			return "", fmt.Errorf("%s=%s: %w", AnsibleRootEnv, abs, err)
		}
		return abs, nil
	}
	if !r.isRelease() {
		if dir, ok := r.walkUp(filepath.Join("ansible", "ansible.cfg")); ok {
			return filepath.Join(dir, "ansible"), nil
		}
	}
	if !embeddedHas(r.ansible, "ansible.cfg") {
		return "", fmt.Errorf("this CLI build (%s) has no embedded Ansible content; build it with `make build-bin-cli` or set %s", r.version, AnsibleRootEnv)
	}
	return r.extractAnsible()
}

func (r *resolver) skipperSources() (fs.FS, error) {
	if !r.isRelease() {
		if dir, ok := r.walkUp(filepath.Join("config", "skipper")); ok {
			return os.DirFS(filepath.Join(dir, "config", "skipper")), nil
		}
	}
	if !embeddedHas(r.skipper, "sitemaps") {
		return nil, fmt.Errorf("this CLI build (%s) has no embedded Skipper sources; build it with `make build-bin-cli`", r.version)
	}
	return r.skipper, nil
}

// walkUp returns the first directory at or above the working directory that
// contains rel.
func (r *resolver) walkUp(rel string) (string, bool) {
	dir, err := r.getwd()
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, rel)); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func embeddedHas(fsys fs.FS, name string) bool {
	if fsys == nil {
		return false
	}
	_, err := fs.Stat(fsys, name)
	return err == nil
}

// extractAnsible materializes the embedded tree under
// <user cache>/frameworks/ansible-bundle/<version>-<content hash>/. The
// directory is written under a temporary name and renamed into place, so an
// existing directory is always complete and is reused as-is; concurrent CLIs
// racing on first use each build a private copy and the loser discards its own.
func (r *resolver) extractAnsible() (string, error) {
	hash, err := r.contentHash()
	if err != nil {
		return "", err
	}
	cacheRoot, err := r.cacheRoot()
	if err != nil {
		return "", fmt.Errorf("locate user cache dir: %w", err)
	}
	parent := filepath.Join(cacheRoot, "frameworks", "ansible-bundle")
	final := filepath.Join(parent, sanitizeVersion(r.version)+"-"+hash[:16])
	if _, statErr := os.Stat(filepath.Join(final, "ansible.cfg")); statErr == nil {
		return final, nil
	}
	if mkErr := os.MkdirAll(parent, 0o755); mkErr != nil {
		return "", fmt.Errorf("create %s: %w", parent, mkErr)
	}
	tmp, err := os.MkdirTemp(parent, ".extract-")
	if err != nil {
		return "", fmt.Errorf("create extract dir: %w", err)
	}
	defer os.RemoveAll(tmp)
	if err := writeTree(r.ansible, tmp); err != nil {
		return "", fmt.Errorf("extract embedded ansible tree: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		if _, statErr := os.Stat(filepath.Join(final, "ansible.cfg")); statErr == nil {
			return final, nil
		}
		return "", fmt.Errorf("install ansible tree at %s: %w", final, err)
	}
	return final, nil
}

func (r *resolver) contentHash() (string, error) {
	r.hashOnce.Do(func() { r.hash, r.hashErr = hashTree(r.ansible) })
	return r.hash, r.hashErr
}

func hashTree(fsys fs.FS) (string, error) {
	var files []string
	if err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	}); err != nil {
		return "", fmt.Errorf("hash embedded ansible tree: %w", err)
	}
	sort.Strings(files)
	h := sha256.New()
	for _, p := range files {
		f, err := fsys.Open(p)
		if err != nil {
			return "", err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", p, info.Size())
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func writeTree(fsys fs.FS, dest string) error {
	return fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dest, filepath.FromSlash(p))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

func sanitizeVersion(v string) string {
	v = strings.Map(func(c rune) rune {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '.', c == '-', c == '_':
			return c
		}
		return '_'
	}, v)
	if v == "" {
		return "unknown"
	}
	return v
}

// SkipperFile is one Skipper source file, keyed by its slash path relative to
// the Skipper source root (e.g. "sitemaps/frameworks.txt").
type SkipperFile struct {
	Path    string
	Content []byte
}

// ReadSkipperFiles returns every file under sitemaps/ then faq/ of
// SkipperSources, each directory in lexical walk order.
func ReadSkipperFiles() ([]SkipperFile, error) {
	fsys, err := SkipperSources()
	if err != nil {
		return nil, err
	}
	return readSkipperTree(fsys)
}

func readSkipperTree(fsys fs.FS) ([]SkipperFile, error) {
	var out []SkipperFile
	for _, dir := range []string{"sitemaps", "faq"} {
		if _, err := fs.Stat(fsys, dir); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("stat %s: %w", dir, err)
		}
		if err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			data, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			out = append(out, SkipperFile{Path: p, Content: data})
			return nil
		}); err != nil {
			return nil, fmt.Errorf("read %s: %w", dir, err)
		}
	}
	return out, nil
}
