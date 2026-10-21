package provisioner

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"

	"frameworks/cli/internal/runtimeassets"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/maintenance"
)

const (
	edgeRenderRolesDir    = "collections/ansible_collections/frameworks/infra/roles"
	edgeRenderPluginsDir  = "collections/ansible_collections/frameworks/infra/plugins"
	edgeRenderPlaybook    = "playbooks/edge.yml"
	edgeRenderRequirement = "requirements.yml"
)

var edgeRenderRoleRef = regexp.MustCompile(`frameworks\.infra\.([a-z0-9_]+)`)

// EdgeRenderDigest identifies the edge render logic this CLI ships: the edge
// playbook, the frameworks.infra edge role and every collection role it
// references (transitively), the collection plugins, requirements.yml, the
// maintenance page native Caddy serves, and the role variables the CLI
// derives in Go, evaluated on a fixed fixture (edgeRenderVarsFixture). It
// reads the tree `edge provision` runs (runtimeassets.AnsibleFS), so two CLIs
// with the same digest render the same provisioned files from the same node
// inputs. The node inputs and release data themselves are not part of it.
func EdgeRenderDigest() (string, error) {
	fsys, err := runtimeassets.AnsibleFS()
	if err != nil {
		return "", err
	}
	return edgeRenderDigest(fsys, maintenance.HTML, edgeRoleVarsFrom)
}

// edgeRoleVarsFunc is the shape of edgeRoleVarsFrom.
type edgeRoleVarsFunc func(config *EdgeProvisionConfig, remoteOS, remoteArch string, src edgeRoleSources) (map[string]any, error)

func edgeRenderDigest(fsys fs.FS, maintenancePage []byte, deriveVars edgeRoleVarsFunc) (string, error) {
	derived, err := edgeRenderVarsFixture(deriveVars)
	if err != nil {
		return "", err
	}
	files := map[string]bool{edgeRenderPlaybook: true, edgeRenderRequirement: true}
	plugins, err := edgeRenderTreeFiles(fsys, edgeRenderPluginsDir)
	if err != nil {
		return "", err
	}
	for _, p := range plugins {
		files[p] = true
	}

	seen := map[string]bool{"edge": true}
	queue := []string{"edge"}
	for len(queue) > 0 {
		role := queue[0]
		queue = queue[1:]
		roleFiles, err := edgeRenderTreeFiles(fsys, path.Join(edgeRenderRolesDir, role))
		if err != nil {
			return "", err
		}
		if len(roleFiles) == 0 {
			return "", fmt.Errorf("edge render digest: role %s has no files", role)
		}
		for _, p := range roleFiles {
			files[p] = true
			data, err := fs.ReadFile(fsys, p)
			if err != nil {
				return "", fmt.Errorf("edge render digest: %w", err)
			}
			for _, m := range edgeRenderRoleRef.FindAllStringSubmatch(string(data), -1) {
				ref := m[1]
				if seen[ref] {
					continue
				}
				// Names that are not roles (modules, plugins) have no role dir.
				if info, statErr := fs.Stat(fsys, path.Join(edgeRenderRolesDir, ref)); statErr != nil || !info.IsDir() {
					continue
				}
				seen[ref] = true
				queue = append(queue, ref)
			}
		}
	}

	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		data, err := fs.ReadFile(fsys, p)
		if err != nil {
			return "", fmt.Errorf("edge render digest: %w", err)
		}
		fmt.Fprintf(h, "%s\x00%d\x00", p, len(data))
		h.Write(data)
	}
	fmt.Fprintf(h, "maintenance.html\x00%d\x00", len(maintenancePage))
	h.Write(maintenancePage)
	fmt.Fprintf(h, "role-vars.json\x00%d\x00", len(derived))
	h.Write(derived)
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

// edgeRenderTreeFiles lists the files under dir that the CLI embeds, with the
// same exclusions as scripts/cli-embed-assets.sh, so a development build
// reading the checkout and a release build reading its embedded tree agree.
// A missing dir has no files.
func edgeRenderTreeFiles(fsys fs.FS, dir string) ([]string, error) {
	if _, err := fs.Stat(fsys, dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("edge render digest: %w", err)
	}
	var out []string
	err := fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			if name == "molecule" || name == "__pycache__" {
				return fs.SkipDir
			}
			return nil
		}
		if name == ".DS_Store" || strings.HasSuffix(name, ".pyc") {
			return nil
		}
		out = append(out, p)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("edge render digest: %w", err)
	}
	return out, nil
}
