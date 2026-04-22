package ansible

import (
	"fmt"
	"sort"
	"strings"
)

// GossBinaryPath is the on-host install location for the goss validator.
const GossBinaryPath = "/usr/local/bin/goss"

// GossSpecDir is where per-service goss.yaml specs live on the host.
const GossSpecDir = "/etc/frameworks/goss"

// GossInstallTasks returns tasks that fetch the pinned goss binary (URL +
// checksum from the release manifest's goss infrastructure entry) and install
// it at GossBinaryPath. Idempotent: get_url skips via checksum, the file mode
// task is naturally idempotent.
func GossInstallTasks(url, checksum string) []Task {
	return []Task{
		TaskGetURL(url, GossBinaryPath, checksum),
		{
			Name:   "ensure goss binary is executable",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": GossBinaryPath, "mode": "0755", "owner": "root", "group": "root"},
		},
	}
}

// GossSpec is a minimal YAML-emitter for the subset of goss assertions the
// post-provision validators use: service running, port listening, file exists,
// process running. It deliberately supports only what the codebase needs — a
// full goss type covering every goss directive would be dead weight.
type GossSpec struct {
	Services  map[string]GossService
	Ports     map[string]GossPort
	Files     map[string]GossFile
	Processes map[string]GossProcess
}

// GossService asserts the systemd (or launchd) unit state.
type GossService struct {
	Running bool
	Enabled bool
}

// GossPort asserts a TCP listener on the given port number (address defaults
// to any host address, which matches 0.0.0.0 / :: in goss semantics).
type GossPort struct {
	Listening bool
	IP        []string // optional; empty → goss wildcard
}

// GossFile asserts file presence and (optionally) ownership+mode.
type GossFile struct {
	Exists bool
	Owner  string
	Group  string
	Mode   string
}

// GossProcess asserts a running process by its command/name.
type GossProcess struct {
	Running bool
}

// RenderGossYAML serializes spec to goss's YAML schema. Keys are sorted so
// output is deterministic across runs (TaskCopy diffs stay clean).
func RenderGossYAML(spec GossSpec) string {
	var b strings.Builder
	writeSection := func(header string, keys []string, body func(k string)) {
		if len(keys) == 0 {
			return
		}
		sort.Strings(keys)
		b.WriteString(header)
		b.WriteString(":\n")
		for _, k := range keys {
			body(k)
		}
	}

	svcKeys := mapKeys(spec.Services)
	writeSection("service", svcKeys, func(k string) {
		s := spec.Services[k]
		fmt.Fprintf(&b, "  %s:\n    running: %t\n    enabled: %t\n", k, s.Running, s.Enabled)
	})

	portKeys := mapKeys(spec.Ports)
	writeSection("port", portKeys, func(k string) {
		p := spec.Ports[k]
		fmt.Fprintf(&b, "  %s:\n    listening: %t\n", k, p.Listening)
		if len(p.IP) > 0 {
			b.WriteString("    ip:\n")
			for _, ip := range p.IP {
				fmt.Fprintf(&b, "      - %s\n", ip)
			}
		}
	})

	fileKeys := mapKeys(spec.Files)
	writeSection("file", fileKeys, func(k string) {
		f := spec.Files[k]
		fmt.Fprintf(&b, "  %s:\n    exists: %t\n", k, f.Exists)
		if f.Owner != "" {
			fmt.Fprintf(&b, "    owner: %s\n", f.Owner)
		}
		if f.Group != "" {
			fmt.Fprintf(&b, "    group: %s\n", f.Group)
		}
		if f.Mode != "" {
			fmt.Fprintf(&b, "    mode: %q\n", f.Mode)
		}
	})

	procKeys := mapKeys(spec.Processes)
	writeSection("process", procKeys, func(k string) {
		p := spec.Processes[k]
		fmt.Fprintf(&b, "  %s:\n    running: %t\n", k, p.Running)
	})

	return b.String()
}

// GossValidateTasks returns: write the spec to /etc/frameworks/goss/<name>.yaml
// and run `goss -g <spec> validate`. Validate is read-only, so ChangedWhen is
// "false" (safe to rerun, never reports changed).
func GossValidateTasks(serviceName, specYAML string) []Task {
	specPath := fmt.Sprintf("%s/%s.yaml", GossSpecDir, serviceName)
	return []Task{
		{
			Name:   "ensure goss spec dir",
			Module: "ansible.builtin.file",
			Args:   map[string]any{"path": GossSpecDir, "state": "directory", "mode": "0755"},
		},
		TaskCopy(specPath, specYAML, CopyOpts{Mode: "0644"}),
		TaskShell(fmt.Sprintf("%s -g %s validate", GossBinaryPath, specPath),
			ShellOpts{ChangedWhen: "false"}),
	}
}

func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
