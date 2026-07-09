// Package edgeseed prepares the single-image edge container on startup: it
// lays out directories/ownership, installs the image-baked component
// binaries onto the persistent volumes, and seeds bootstrap config. It runs
// as the s6 init-seed oneshot (root) before any service starts.
package edgeseed

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"frameworks/api_sidecar/internal/updater"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/maintenance"
)

const (
	distRoot            = "/usr/share/frameworks/dist"
	optRoot             = "/opt/frameworks"
	etcFrameworks       = "/etc/frameworks"
	etcCaddy            = "/etc/caddy"
	imageSeededVersions = "/etc/frameworks/image-seeded-versions.env"
	bootstrapCaddyfile  = "/etc/frameworks/templates/Caddyfile.bootstrap"
)

type component struct {
	name       string // updater component name (component-versions.env key owner)
	distPath   string // source under distRoot
	installDir string // target under optRoot
	probe      string // file whose absence forces installation
	tree       bool   // whole-tree payload (vs single binary)
}

var components = []component{
	{name: "helmsman", distPath: "helmsman/helmsman", installDir: "helmsman", probe: "helmsman", tree: false},
	{name: "mist", distPath: "mistserver", installDir: "mistserver", probe: filepath.Join("bin", "MistController"), tree: true},
	{name: "caddy", distPath: "caddy/caddy", installDir: "caddy", probe: "caddy", tree: false},
}

func Run() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("seed-edge only runs inside the Linux edge container")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("seed-edge must run as root")
	}

	fw, caddyIDs, err := lookupAccounts()
	if err != nil {
		return err
	}
	if layoutErr := ensureLayout(fw, caddyIDs); layoutErr != nil {
		return layoutErr
	}

	baked, err := readEnvFile(filepath.Join(distRoot, "versions.env"))
	if err != nil {
		return fmt.Errorf("image dist versions: %w", err)
	}
	installed := updater.ReadComponentVersions()
	seeded, err := readEnvFile(imageSeededVersions)
	if err != nil {
		return fmt.Errorf("image-seeded versions: %w", err)
	}

	for _, comp := range components {
		key, err := updater.ComponentVersionKey(comp.name)
		if err != nil {
			return err
		}
		bakedVersion := strings.TrimSpace(baked[key])
		if bakedVersion == "" {
			return fmt.Errorf("dist versions.env is missing %s", key)
		}
		if !shouldInstall(comp, installed[key], seeded[key], bakedVersion) {
			continue
		}
		if err := installComponent(comp, fw); err != nil {
			return fmt.Errorf("install %s: %w", comp.name, err)
		}
		if err := updater.WriteComponentVersion(comp.name, bakedVersion); err != nil {
			return err
		}
		seeded[key] = bakedVersion
		if err := writeEnvFile(imageSeededVersions, seeded); err != nil {
			return err
		}
		fmt.Printf("seed-edge: installed %s %s\n", comp.name, bakedVersion)
	}

	if schema := strings.TrimSpace(baked["CONFIG_SCHEMA_VERSION"]); schema != "" && strings.TrimSpace(installed["CONFIG_SCHEMA_VERSION"]) == "" {
		if err := updater.WriteComponentVersion("config_schema", schema); err != nil {
			return err
		}
	}

	if err := seedBootstrapConfig(fw, caddyIDs); err != nil {
		return err
	}
	probeAtomicSwap()
	return nil
}

// shouldInstall implements the image-seeded marker rule: the seed only ever
// replaces a version that a previous image put there, so a Foghorn-pushed
// version (newer or deliberately pinned older) is never touched and the
// release reconciler stays the source of truth.
func shouldInstall(comp component, installedVersion, seededVersion, bakedVersion string) bool {
	if _, err := os.Stat(filepath.Join(optRoot, comp.installDir, comp.probe)); err != nil {
		return true
	}
	installedVersion = strings.TrimSpace(installedVersion)
	if installedVersion == "" {
		return true
	}
	return installedVersion == strings.TrimSpace(seededVersion) && installedVersion != bakedVersion
}

func installComponent(comp component, fw ids) error {
	src := filepath.Join(distRoot, comp.distPath)
	dst := filepath.Join(optRoot, comp.installDir)
	if comp.tree {
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
		if err := copyTree(src, dst); err != nil {
			return err
		}
	} else {
		if err := os.MkdirAll(dst, 0o755); err != nil {
			return err
		}
		if err := copyFile(src, filepath.Join(dst, comp.probe), 0o755); err != nil {
			return err
		}
	}
	return chownTree(dst, fw.uid, fw.gid)
}

func seedBootstrapConfig(fw, caddyIDs ids) error {
	caddyfile := filepath.Join(etcCaddy, "Caddyfile")
	if _, err := os.Stat(caddyfile); err != nil {
		if err := copyFile(bootstrapCaddyfile, caddyfile, 0o644); err != nil {
			return fmt.Errorf("bootstrap Caddyfile: %w", err)
		}
		if err := os.Chown(caddyfile, fw.uid, caddyIDs.gid); err != nil {
			return err
		}
	}

	// RenderCaddyfile's handle_errors block serves this page, so it must
	// exist before the first activation.
	maintenancePath := filepath.Join(etcCaddy, "maintenance.html")
	if _, err := os.Stat(maintenancePath); err != nil {
		if err := os.WriteFile(maintenancePath, []byte(maintenance.HTML), 0o644); err != nil {
			return err
		}
		if err := os.Chown(maintenancePath, fw.uid, caddyIDs.gid); err != nil {
			return err
		}
	}

	mistConf := filepath.Join(etcFrameworks, "mistserver.conf")
	if _, err := os.Stat(mistConf); err != nil {
		seedConf := "{\"config\":{\"controller\":{\"interface\":\"127.0.0.1\",\"port\":4242}}}\n"
		if err := os.WriteFile(mistConf, []byte(seedConf), 0o644); err != nil {
			return err
		}
		if err := os.Chown(mistConf, fw.uid, fw.gid); err != nil {
			return err
		}
	}
	return nil
}

type ids struct {
	uid, gid int
}

func lookupAccounts() (ids, ids, error) {
	fwUser, err := user.Lookup("frameworks")
	if err != nil {
		return ids{}, ids{}, fmt.Errorf("frameworks user: %w", err)
	}
	caddyGroup, err := user.LookupGroup("caddy")
	if err != nil {
		return ids{}, ids{}, fmt.Errorf("caddy group: %w", err)
	}
	caddyUser, err := user.Lookup("caddy")
	if err != nil {
		return ids{}, ids{}, fmt.Errorf("caddy user: %w", err)
	}
	fwUID, err := strconv.Atoi(fwUser.Uid)
	if err != nil {
		return ids{}, ids{}, err
	}
	fwGID, err := strconv.Atoi(fwUser.Gid)
	if err != nil {
		return ids{}, ids{}, err
	}
	caddyUID, err := strconv.Atoi(caddyUser.Uid)
	if err != nil {
		return ids{}, ids{}, err
	}
	caddyGID, err := strconv.Atoi(caddyGroup.Gid)
	if err != nil {
		return ids{}, ids{}, err
	}
	return ids{uid: fwUID, gid: fwGID}, ids{uid: caddyUID, gid: caddyGID}, nil
}

type dirSpec struct {
	path string
	uid  int
	gid  int
	mode os.FileMode
	// optional dirs are host bind mounts whose ownership the operator may
	// control; failing to adjust them must not block boot.
	optional bool
}

// ensureLayout mirrors the native install's directory contract
// (roles/edge + roles/helmsman): frameworks owns /opt and /etc/frameworks,
// certs and /etc/caddy are group-shared with caddy, and /opt/frameworks must
// be frameworks-writable because the updater stages sibling dirs there for
// atomic swaps.
func ensureLayout(fw, caddyIDs ids) error {
	specs := []dirSpec{
		{path: optRoot, uid: fw.uid, gid: fw.gid, mode: 0o755},
		{path: etcFrameworks, uid: fw.uid, gid: fw.gid, mode: 0o755},
		{path: filepath.Join(etcFrameworks, "certs"), uid: fw.uid, gid: caddyIDs.gid, mode: 0o2770},
		{path: filepath.Join(etcFrameworks, "certs", "bundles"), uid: fw.uid, gid: caddyIDs.gid, mode: 0o2770},
		// pki and telemetry are host bind mounts in the compose layout (CA
		// staged by the provisioner; token shared with vmagent).
		{path: filepath.Join(etcFrameworks, "pki"), uid: fw.uid, gid: fw.gid, mode: 0o750, optional: true},
		{path: filepath.Join(etcFrameworks, "telemetry"), uid: fw.uid, gid: fw.gid, mode: 0o750, optional: true},
		{path: etcCaddy, uid: fw.uid, gid: caddyIDs.gid, mode: 0o2775},
		{path: "/var/lib/caddy", uid: caddyIDs.uid, gid: caddyIDs.gid, mode: 0o750},
		{path: "/run/caddy", uid: caddyIDs.uid, gid: caddyIDs.gid, mode: 0o770},
		{path: "/data/storage", uid: fw.uid, gid: fw.gid, mode: 0o750},
	}
	for _, spec := range specs {
		// Chown BEFORE tightening the mode: on optional bind mounts
		// (host/FUSE) chown is what fails, and a half-applied chmod 0750
		// with root:root left behind would lock the frameworks user out of
		// the CA bundle / telemetry token entirely.
		err := os.MkdirAll(spec.path, spec.mode)
		if err == nil {
			err = os.Chown(spec.path, spec.uid, spec.gid)
		}
		if err == nil {
			err = os.Chmod(spec.path, spec.mode)
		}
		if err != nil {
			if spec.optional {
				fmt.Printf("seed-edge: WARNING: could not adjust %s (%v); continuing\n", spec.path, err)
				continue
			}
			return err
		}
	}
	return nil
}

// probeAtomicSwap verifies RENAME_EXCHANGE works on the /opt/frameworks
// filesystem; the in-place MistServer update depends on it. Named volumes
// pass; NFS/FUSE bind mounts do not, which deserves a loud warning rather
// than a first-update surprise.
func probeAtomicSwap() {
	a, err := os.MkdirTemp(optRoot, ".swap-probe-a-*")
	if err != nil {
		fmt.Printf("seed-edge: WARNING: atomic-swap probe setup failed: %v\n", err)
		return
	}
	defer func() { _ = os.RemoveAll(a) }()
	b, err := os.MkdirTemp(optRoot, ".swap-probe-b-*")
	if err != nil {
		fmt.Printf("seed-edge: WARNING: atomic-swap probe setup failed: %v\n", err)
		return
	}
	defer func() { _ = os.RemoveAll(b) }()
	if err := updater.ExchangeDirsForProbe(a, b); err != nil {
		fmt.Printf("seed-edge: WARNING: %s does not support atomic directory exchange (%v); in-place MistServer updates will fail — use a local-filesystem volume\n", optRoot, err)
	}
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	tmp := dst + ".seed-tmp"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
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
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode()&os.ModeSymlink != 0:
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_ = os.Remove(target)
			return os.Symlink(link, target)
		default:
			return copyFile(path, target, info.Mode().Perm())
		}
	})
}

func chownTree(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return os.Lchown(path, uid, gid)
		}
		return os.Chown(path, uid, gid)
	})
}

func readEnvFile(path string) (map[string]string, error) {
	out := map[string]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && k != "" {
			out[k] = v
		}
	}
	return out, nil
}

func writeEnvFile(path string, values map[string]string) error {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	var sb strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&sb, "%s=%s\n", k, values[k])
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
