package ansiblerun

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"gopkg.in/yaml.v3"
)

// Host describes a single target in the rendered inventory.
//
// Only Name and Address are required. The rest map to ansible_* connection
// variables; leaving them empty lets Ansible fall back to ansible.cfg defaults
// or ~/.ssh/config. That fallback is deliberate and aligns with
// feedback_dont_override_local_config: don't stomp on local SSH config.
type Host struct {
	// Name is the inventory_hostname used by Ansible (must be a valid
	// Ansible identifier: letters, digits, underscore, dash, dot).
	Name string

	// Address is the ansible_host value. For localhost runs set to
	// "localhost" or "127.0.0.1".
	Address string

	// User maps to ansible_user. Empty = fall through to ssh_config.
	User string

	// Port maps to ansible_port. Zero = default 22.
	Port int

	// PrivateKey maps to ansible_ssh_private_key_file. Empty = default.
	PrivateKey string

	// Connection maps to ansible_connection. Empty = ssh. Set to "local"
	// for localhost runs to skip SSH entirely.
	Connection string

	// Vars are per-host variables, written inline alongside the connection
	// vars in the inventory file.
	Vars map[string]any
}

// Group is a named collection of hosts with optional group-wide vars.
//
// GroupVars write to group_vars/<Name>.yml, which is the idiomatic location
// Ansible scans automatically when the inventory directory is passed via -i.
type Group struct {
	Name  string
	Hosts []string // references Host.Name
	Vars  map[string]any
}

// Inventory is the input to InventoryRenderer.Render.
type Inventory struct {
	Hosts       []Host
	Groups      []Group
	GlobalVars  map[string]any // written to group_vars/all.yml
	GlobalFacts map[string]any // written to group_vars/all.yml under the same top-level
}

// InventoryRenderer writes an Ansible-compatible inventory directory.
//
// Layout:
//
//	<dir>/
//	  inventory.yml        — YAML inventory with all groups + per-host vars
//	  group_vars/
//	    all.yml            — GlobalVars merged with GlobalFacts
//	    <group>.yml        — one file per Group with non-nil Vars
//
// The caller passes <dir>/inventory.yml as ansible-playbook's -i argument.
// Ansible automatically scans <dir>/group_vars/ relative to the inventory.
type InventoryRenderer struct{}

// Render writes inventory.yml and group_vars/*.yml into dir. dir must exist
// and be writable; typically created by the caller as an os.MkdirTemp child.
// Returns the absolute path of the inventory.yml file.
func (r *InventoryRenderer) Render(dir string, inv Inventory) (string, error) {
	if dir == "" {
		return "", errors.New("ansiblerun: inventory dir is required")
	}
	if len(inv.Hosts) == 0 {
		return "", errors.New("ansiblerun: inventory has no hosts")
	}

	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve inventory dir: %w", err)
	}

	hostsByName := make(map[string]Host, len(inv.Hosts))
	for _, h := range inv.Hosts {
		if h.Name == "" {
			return "", errors.New("ansiblerun: host with empty Name")
		}
		if h.Address == "" {
			return "", fmt.Errorf("ansiblerun: host %q has empty Address", h.Name)
		}
		if _, dup := hostsByName[h.Name]; dup {
			return "", fmt.Errorf("ansiblerun: duplicate host name %q", h.Name)
		}
		hostsByName[h.Name] = h
	}

	invTree := buildInventoryTree(inv, hostsByName)
	invPath := filepath.Join(dir, "inventory.yml")
	if err := writeYAML(invPath, invTree); err != nil {
		return "", fmt.Errorf("write inventory.yml: %w", err)
	}

	if err := writeGroupVars(dir, inv); err != nil {
		return "", err
	}

	return invPath, nil
}

func buildInventoryTree(inv Inventory, hostsByName map[string]Host) map[string]any {
	allHosts := make(map[string]any, len(inv.Hosts))
	for _, h := range inv.Hosts {
		allHosts[h.Name] = hostEntry(h)
	}

	children := make(map[string]any, len(inv.Groups))
	for _, g := range inv.Groups {
		groupHosts := make(map[string]any, len(g.Hosts))
		for _, name := range g.Hosts {
			if _, ok := hostsByName[name]; !ok {
				// Tolerate missing host references by omitting them; the
				// caller's rendering code should surface this upstream,
				// not silently materialize a phantom host entry.
				continue
			}
			groupHosts[name] = map[string]any{}
		}
		entry := map[string]any{"hosts": groupHosts}
		children[g.Name] = entry
	}

	all := map[string]any{"hosts": allHosts}
	if len(children) > 0 {
		all["children"] = children
	}
	return map[string]any{"all": all}
}

func hostEntry(h Host) map[string]any {
	out := map[string]any{"ansible_host": h.Address}
	if h.User != "" {
		out["ansible_user"] = h.User
	}
	if h.Port != 0 {
		out["ansible_port"] = h.Port
	}
	if h.PrivateKey != "" {
		out["ansible_ssh_private_key_file"] = h.PrivateKey
	}
	if h.Connection != "" {
		out["ansible_connection"] = h.Connection
	}
	maps.Copy(out, h.Vars)
	return out
}

func writeGroupVars(dir string, inv Inventory) error {
	gvDir := filepath.Join(dir, "group_vars")
	if err := os.MkdirAll(gvDir, 0o755); err != nil {
		return fmt.Errorf("create group_vars dir: %w", err)
	}

	if len(inv.GlobalVars) > 0 || len(inv.GlobalFacts) > 0 {
		merged := make(map[string]any, len(inv.GlobalVars)+len(inv.GlobalFacts))
		maps.Copy(merged, inv.GlobalVars)
		maps.Copy(merged, inv.GlobalFacts)
		if err := writeYAML(filepath.Join(gvDir, "all.yml"), merged); err != nil {
			return fmt.Errorf("write group_vars/all.yml: %w", err)
		}
	}

	groups := slices.Clone(inv.Groups)
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	for _, g := range groups {
		if len(g.Vars) == 0 {
			continue
		}
		path := filepath.Join(gvDir, g.Name+".yml")
		if err := writeYAML(path, g.Vars); err != nil {
			return fmt.Errorf("write group_vars/%s.yml: %w", g.Name, err)
		}
	}
	return nil
}

func writeYAML(path string, v any) error {
	data, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
