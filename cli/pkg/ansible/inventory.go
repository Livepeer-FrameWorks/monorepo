package ansible

import (
	"fmt"
	"sort"
	"strings"

	"frameworks/cli/pkg/inventory"
)

// NewInventory creates a new Ansible inventory
func NewInventory() *Inventory {
	return &Inventory{
		Groups: make(map[string]*InventoryGroup),
		Hosts:  make(map[string]*InventoryHost),
	}
}

// AddHost adds a host to the inventory
func (inv *Inventory) AddHost(host *InventoryHost) {
	inv.Hosts[host.Name] = host
}

// AddGroup adds a group to the inventory
func (inv *Inventory) AddGroup(group *InventoryGroup) {
	inv.Groups[group.Name] = group
}

// ToINI converts the inventory to INI format
func (inv *Inventory) ToINI() string {
	var lines []string

	// Write ungrouped hosts first
	ungroupedHosts := inv.getUngroupedHosts()
	if len(ungroupedHosts) > 0 {
		for _, hostName := range ungroupedHosts {
			host := inv.Hosts[hostName]
			lines = append(lines, formatHost(host))
		}
		lines = append(lines, "")
	}

	// Write groups
	groupNames := make([]string, 0, len(inv.Groups))
	for name := range inv.Groups {
		groupNames = append(groupNames, name)
	}
	sort.Strings(groupNames)

	for _, groupName := range groupNames {
		group := inv.Groups[groupName]
		lines = append(lines, fmt.Sprintf("[%s]", groupName))

		// Write group hosts
		for _, hostName := range group.Hosts {
			if host, exists := inv.Hosts[hostName]; exists {
				lines = append(lines, formatHost(host))
			}
		}

		// Write group vars
		if len(group.Vars) > 0 {
			lines = append(lines, "")
			lines = append(lines, fmt.Sprintf("[%s:vars]", groupName))
			for key, value := range group.Vars {
				lines = append(lines, fmt.Sprintf("%s=%s", key, value))
			}
		}

		// Write group children
		if len(group.Children) > 0 {
			lines = append(lines, "")
			lines = append(lines, fmt.Sprintf("[%s:children]", groupName))
			for _, child := range group.Children {
				lines = append(lines, child)
			}
		}

		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// formatHost formats a host line for INI inventory
func formatHost(host *InventoryHost) string {
	parts := []string{host.Name}

	if host.Address != "" && host.Address != host.Name {
		parts = append(parts, fmt.Sprintf("ansible_host=%s", host.Address))
	}

	// Add host vars
	for key, value := range host.Vars {
		parts = append(parts, fmt.Sprintf("%s=%s", key, value))
	}

	return strings.Join(parts, " ")
}

// getUngroupedHosts returns hosts that are not in any group
func (inv *Inventory) getUngroupedHosts() []string {
	grouped := make(map[string]bool)
	for _, group := range inv.Groups {
		for _, hostName := range group.Hosts {
			grouped[hostName] = true
		}
	}

	var ungrouped []string
	for hostName := range inv.Hosts {
		if !grouped[hostName] {
			ungrouped = append(ungrouped, hostName)
		}
	}

	sort.Strings(ungrouped)
	return ungrouped
}

// FromManifest creates an Ansible inventory from a cluster manifest
func FromManifest(manifest *inventory.Manifest) *Inventory {
	inv := NewInventory()

	// Add all hosts
	for name, host := range manifest.Hosts {
		invHost := &InventoryHost{
			Name:    name,
			Address: host.Address,
			Vars:    make(map[string]string),
		}

		// Add SSH connection vars
		if host.User != "" {
			invHost.Vars["ansible_user"] = host.User
		}

		if host.SSHKey != "" {
			invHost.Vars["ansible_ssh_private_key_file"] = host.SSHKey
		}

		// Add labels as vars
		for k, v := range host.Labels {
			invHost.Vars[fmt.Sprintf("label_%s", k)] = v
		}

		inv.AddHost(invHost)
	}

	// Create groups based on roles
	roleGroups := make(map[string][]string)
	for name, host := range manifest.Hosts {
		for _, role := range host.Roles {
			roleGroups[role] = append(roleGroups[role], name)
		}
	}

	// Add role groups
	for role, hosts := range roleGroups {
		inv.AddGroup(&InventoryGroup{
			Name:  role,
			Hosts: hosts,
			Vars:  make(map[string]string),
		})
	}

	// Add infrastructure group
	infraHosts := []string{}
	for name, host := range manifest.Hosts {
		for _, role := range host.Roles {
			if role == "infrastructure" {
				infraHosts = append(infraHosts, name)
				break
			}
		}
	}

	if len(infraHosts) > 0 {
		inv.AddGroup(&InventoryGroup{
			Name:  "infrastructure",
			Hosts: infraHosts,
			Vars:  make(map[string]string),
		})
	}

	return inv
}
