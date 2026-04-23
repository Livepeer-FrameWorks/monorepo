package ansible

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"strconv"
)

// PackageState is the desired state of an OS package.
type PackageState string

const (
	PackagePresent PackageState = "present"
	PackageAbsent  PackageState = "absent"
	PackageLatest  PackageState = "latest"
)

// CopyOpts configures TaskCopy.
type CopyOpts struct {
	Owner string
	Group string
	Mode  string // octal string, e.g. "0644"
	When  string
}

// SystemdOpts configures TaskSystemdService.
type SystemdOpts struct {
	State        string // "started", "stopped", "restarted", "reloaded"
	Enabled      *bool  // pointer so nil means "don't set"
	DaemonReload bool
	NoBlock      bool
	When         string
}

// WaitForOpts configures TaskWaitForPort.
type WaitForOpts struct {
	Host    string
	Delay   int
	Timeout int
	Sleep   int
	When    string
}

// ShellOpts configures TaskShell. At least one of Creates, Removes, When, or
// ChangedWhen must be set — TaskShell panics otherwise. This keeps the escape
// hatch idempotent by construction. Extra passes rarely-needed shell-module
// args through without widening the struct.
type ShellOpts struct {
	Creates     string
	Removes     string
	Chdir       string
	Environment map[string]string
	When        string
	ChangedWhen string // e.g. "false" for commands always safe to re-run (sysctl --system, systemctl daemon-reload)
	Extra       map[string]any
}

// BoolPtr is a convenience for fields that use *bool to distinguish "unset"
// from "false".
func BoolPtr(v bool) *bool { return &v }

// ArtifactSentinel returns a marker path under dest whose hash suffix rotates
// when identityKey changes. Use as the `creates:` gate for TaskUnarchive so a
// pinned-version bump reliably re-extracts instead of skipping.
func ArtifactSentinel(dest, identityKey string) string {
	h := sha256.Sum256([]byte(identityKey))
	return dest + "/.installed-" + hex.EncodeToString(h[:6])
}

// TaskGetURL emits ansible.builtin.get_url to fetch url to dest and verify
// checksum. checksum format is "<algo>:<hex>" (sha256 or sha512).
func TaskGetURL(url, dest, checksum string) Task {
	args := map[string]any{
		"url":  url,
		"dest": dest,
	}
	if checksum != "" {
		args["checksum"] = checksum
	}
	return Task{
		Name:   "download " + dest,
		Module: "ansible.builtin.get_url",
		Args:   args,
	}
}

// UnarchiveOpts configures TaskUnarchive beyond the mandatory src/dest/creates.
// StripComponents > 0 passes --strip-components=N to tar so archives that
// wrap a single versioned top-dir land contents directly under dest.
type UnarchiveOpts struct {
	StripComponents int
	Owner           string
	Group           string
}

// TaskUnarchive emits ansible.builtin.unarchive with remote_src=true. creates
// is required — it's the file Ansible checks to skip re-extraction on rerun,
// without it a second apply clobbers any in-place state under dest.
func TaskUnarchive(src, dest, creates string, opts UnarchiveOpts) Task {
	if creates == "" {
		panic("ansible.TaskUnarchive: creates must be non-empty (re-extraction guardrail)")
	}
	args := map[string]any{
		"src":        src,
		"dest":       dest,
		"remote_src": true,
		"creates":    creates,
	}
	if opts.StripComponents > 0 {
		args["extra_opts"] = []string{"--strip-components=" + strconv.Itoa(opts.StripComponents)}
	}
	if opts.Owner != "" {
		args["owner"] = opts.Owner
	}
	if opts.Group != "" {
		args["group"] = opts.Group
	}
	return Task{
		Name:   "extract " + src,
		Module: "ansible.builtin.unarchive",
		Args:   args,
	}
}

// TaskCopy emits ansible.builtin.copy with inline content. The Go caller is
// expected to have rendered the file already; Ansible just delivers it.
func TaskCopy(dest, content string, opts CopyOpts) Task {
	args := map[string]any{
		"dest":    dest,
		"content": content,
	}
	if opts.Owner != "" {
		args["owner"] = opts.Owner
	}
	if opts.Group != "" {
		args["group"] = opts.Group
	}
	if opts.Mode != "" {
		args["mode"] = opts.Mode
	}
	return Task{
		Name:   "copy " + dest,
		Module: "ansible.builtin.copy",
		Args:   args,
		When:   opts.When,
	}
}

// TaskPackage emits ansible.builtin.package. Ansible picks the distro's
// package manager; name must still be the concrete distro-correct name
// (use DistroPackageSpec to resolve).
func TaskPackage(name string, state PackageState) Task {
	if state == "" {
		state = PackagePresent
	}
	return Task{
		Name:   "install package " + name,
		Module: "ansible.builtin.package",
		Args: map[string]any{
			"name":  name,
			"state": string(state),
		},
	}
}

// TaskSystemdService emits ansible.builtin.systemd_service.
func TaskSystemdService(name string, opts SystemdOpts) Task {
	args := map[string]any{
		"name": name,
	}
	if opts.State != "" {
		args["state"] = opts.State
	}
	if opts.Enabled != nil {
		args["enabled"] = *opts.Enabled
	}
	if opts.DaemonReload {
		args["daemon_reload"] = true
	}
	if opts.NoBlock {
		args["no_block"] = true
	}
	return Task{
		Name:   "systemd " + name,
		Module: "ansible.builtin.systemd_service",
		Args:   args,
		When:   opts.When,
	}
}

// TaskWaitForPort emits ansible.builtin.wait_for for listener readiness after
// systemd_service. When Host is empty, Ansible defaults to 127.0.0.1, so
// callers must pass the real listener address for services that are not meant
// to be probed via loopback.
func TaskWaitForPort(port int, opts WaitForOpts) Task {
	args := map[string]any{
		"port":  port,
		"state": "started",
	}
	if opts.Host != "" {
		args["host"] = opts.Host
	}
	if opts.Delay > 0 {
		args["delay"] = opts.Delay
	}
	if opts.Timeout > 0 {
		args["timeout"] = opts.Timeout
	}
	if opts.Sleep > 0 {
		args["sleep"] = opts.Sleep
	}
	return Task{
		Name:   "wait for port " + strconv.Itoa(port),
		Module: "ansible.builtin.wait_for",
		Args:   args,
		When:   opts.When,
	}
}

// TaskShell emits ansible.builtin.shell. At least one of Creates, Removes, or
// When must be set — callers that need an unguarded command should explain why
// in a comment at the call site and wrap this with an explicit opts.When="true"
// or similar documented predicate.
func TaskShell(cmd string, opts ShellOpts) Task {
	if opts.Creates == "" && opts.Removes == "" && opts.When == "" && opts.ChangedWhen == "" {
		panic("ansible.TaskShell: at least one of ShellOpts.Creates, .Removes, .When, or .ChangedWhen must be set (idempotence guardrail)")
	}
	args := map[string]any{
		"cmd": cmd,
	}
	if opts.Creates != "" {
		args["creates"] = opts.Creates
	}
	if opts.Removes != "" {
		args["removes"] = opts.Removes
	}
	if opts.Chdir != "" {
		args["chdir"] = opts.Chdir
	}
	maps.Copy(args, opts.Extra)
	return Task{
		Name:        "shell: " + cmd,
		Module:      "ansible.builtin.shell",
		Args:        args,
		When:        opts.When,
		ChangedWhen: opts.ChangedWhen,
		Environment: opts.Environment,
	}
}
