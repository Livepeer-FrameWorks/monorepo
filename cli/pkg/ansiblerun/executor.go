package ansiblerun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	goansible_execute "github.com/apenella/go-ansible/v2/pkg/execute"
	goansible_result "github.com/apenella/go-ansible/v2/pkg/execute/result"
	goansible_playbook "github.com/apenella/go-ansible/v2/pkg/playbook"
)

// ExecuteOptions carries every knob the CLI needs to drive ansible-playbook.
//
// Orchestration concerns (which host, which version, which environment) are
// expressed as ExtraVars and Tags — never encoded in role defaults. Runtime
// concerns (SSH identity, become escalation, verbosity) are expressed as
// top-level fields mapped onto ansible-playbook's CLI flags by go-ansible v2.
type ExecuteOptions struct {
	// Playbook is the absolute path to the playbook YAML. Required.
	Playbook string

	// Inventory is the absolute path to an inventory file rendered by
	// InventoryRenderer (or supplied by the caller). Required.
	Inventory string

	// ExtraVars are written to a 0600 temp file and passed as
	// `--extra-vars=@file` so secrets do not appear in process arguments or
	// go-ansible's enriched failure output.
	ExtraVars map[string]any

	// Tags restricts the play to tasks tagged with any of these.
	Tags []string

	// SkipTags excludes tasks tagged with any of these.
	SkipTags []string

	// Limit restricts the play to a subset of hosts (Ansible limit pattern).
	Limit string

	// Check runs in --check mode (no state changes).
	Check bool

	// Diff shows --diff output alongside --check.
	Diff bool

	// Verbose selects -v..-vvvv. 0 is silent (default).
	Verbose int

	// PrivateKey is the SSH private key path. Left empty lets ~/.ssh/config
	// or ssh-agent decide — aligns with feedback_dont_override_local_config.
	PrivateKey string

	// User overrides the SSH user. Usually set per-host in inventory; leave
	// empty unless the caller is certain they want a play-wide override.
	User string

	// Become enables privilege escalation. BecomeUser optionally sets the
	// target (default: root).
	Become     bool
	BecomeUser string

	// WorkDir is the cwd of the ansible-playbook subprocess. Typically the
	// ansible/ tree root so ansible.cfg is picked up automatically.
	WorkDir string

	// EnvVars is merged into the subprocess environment. Callers MUST NOT
	// rely on this for secret material — pass SOPS_AGE_KEY_FILE and similar
	// via the parent process environment (inheritance) so that they flow
	// through community.sops transparently.
	EnvVars map[string]string

	// Outputer lets callers customize how stdout/stderr is rendered. Nil
	// means passthrough to the process's stdout/stderr.
	Outputer goansible_result.ResultsOutputer
}

// Executor is a thin wrapper around go-ansible v2's playbook.AnsiblePlaybookCmd
// + execute.DefaultExecute. It holds no state beyond the ansible-playbook
// binary path.
type Executor struct {
	// Binary overrides the ansible-playbook binary. Empty means the default
	// ("ansible-playbook" on PATH).
	Binary string
}

// NewExecutor returns an Executor that resolves ansible-playbook from PATH.
// It errors out loudly if the binary is missing — this is the CLI's single
// preflight point for the Ansible runtime dependency.
func NewExecutor() (*Executor, error) {
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		return nil, fmt.Errorf("ansible-playbook not found on PATH: %w", err)
	}
	return &Executor{}, nil
}

// Execute runs the playbook. It returns a non-nil error if any host failed or
// was unreachable, or if the subprocess itself couldn't be started. Callers
// MUST treat a non-nil error as a failed apply — no partial-success masking.
func (e *Executor) Execute(ctx context.Context, opts ExecuteOptions) error {
	if opts.Playbook == "" {
		return errors.New("ansiblerun: Playbook is required")
	}
	if opts.Inventory == "" {
		return errors.New("ansiblerun: Inventory is required")
	}

	extraVarsFile, cleanup, err := writeExtraVarsFile(opts.ExtraVars)
	if err != nil {
		return err
	}
	defer cleanup()

	playOpts := &goansible_playbook.AnsiblePlaybookOptions{
		Inventory:     opts.Inventory,
		ExtraVarsFile: extraVarsFile,
		Tags:          strings.Join(opts.Tags, ","),
		SkipTags:      strings.Join(opts.SkipTags, ","),
		Limit:         opts.Limit,
		Check:         opts.Check,
		Diff:          opts.Diff,
		PrivateKey:    opts.PrivateKey,
		User:          opts.User,
		Become:        opts.Become,
		BecomeUser:    opts.BecomeUser,
	}
	applyVerbosity(playOpts, opts.Verbose)

	cmdOpts := []goansible_playbook.AnsiblePlaybookOptionsFunc{
		goansible_playbook.WithPlaybooks(opts.Playbook),
		goansible_playbook.WithPlaybookOptions(playOpts),
	}
	if e.Binary != "" {
		cmdOpts = append(cmdOpts, goansible_playbook.WithBinary(e.Binary))
	}
	cmd := goansible_playbook.NewAnsiblePlaybookCmd(cmdOpts...)

	execOpts := []goansible_execute.ExecuteOptions{
		goansible_execute.WithCmd(cmd),
		goansible_execute.WithErrorEnrich(goansible_playbook.NewAnsiblePlaybookErrorEnrich()),
	}
	if opts.WorkDir != "" {
		execOpts = append(execOpts, goansible_execute.WithCmdRunDir(opts.WorkDir))
	}
	if len(opts.EnvVars) > 0 {
		execOpts = append(execOpts, goansible_execute.WithEnvVars(opts.EnvVars))
	}
	if opts.Outputer != nil {
		execOpts = append(execOpts, goansible_execute.WithOutput(opts.Outputer))
	} else {
		execOpts = append(execOpts,
			goansible_execute.WithWrite(os.Stdout),
			goansible_execute.WithWriteError(os.Stderr),
		)
	}
	runner := goansible_execute.NewDefaultExecute(execOpts...)

	return runner.Execute(ctx)
}

func applyVerbosity(opts *goansible_playbook.AnsiblePlaybookOptions, level int) {
	switch {
	case level >= 4:
		opts.VerboseVVVV = true
	case level == 3:
		opts.VerboseVVV = true
	case level == 2:
		opts.VerboseVV = true
	case level == 1:
		opts.VerboseV = true
	}
}

func writeExtraVarsFile(vars map[string]any) ([]string, func(), error) {
	if len(vars) == 0 {
		return nil, func() {}, nil
	}
	f, err := os.CreateTemp("", "frameworks-ansible-vars-*.json")
	if err != nil {
		return nil, nil, fmt.Errorf("ansiblerun: create extra-vars file: %w", err)
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }
	if err := os.Chmod(path, 0o600); err != nil {
		_ = f.Close()
		cleanup()
		return nil, nil, fmt.Errorf("ansiblerun: secure extra-vars file: %w", err)
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(vars); err != nil {
		_ = f.Close()
		cleanup()
		return nil, nil, fmt.Errorf("ansiblerun: write extra-vars file: %w", err)
	}
	if err := f.Close(); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("ansiblerun: close extra-vars file: %w", err)
	}
	return []string{"@" + path}, cleanup, nil
}

// Preview returns the argv that Execute would run without running it. Useful
// for --dry-run-of-the-wrapper diagnostics and for surfacing the full command
// line on failures (feedback_suspect_wrapper_before_env).
func (e *Executor) Preview(opts ExecuteOptions) ([]string, error) {
	playOpts := &goansible_playbook.AnsiblePlaybookOptions{
		Inventory:  opts.Inventory,
		Tags:       strings.Join(opts.Tags, ","),
		SkipTags:   strings.Join(opts.SkipTags, ","),
		Limit:      opts.Limit,
		Check:      opts.Check,
		Diff:       opts.Diff,
		PrivateKey: opts.PrivateKey,
		User:       opts.User,
		Become:     opts.Become,
		BecomeUser: opts.BecomeUser,
	}
	if len(opts.ExtraVars) > 0 {
		playOpts.ExtraVarsFile = []string{"@<extra-vars-file>"}
	}
	applyVerbosity(playOpts, opts.Verbose)

	cmdOpts := []goansible_playbook.AnsiblePlaybookOptionsFunc{
		goansible_playbook.WithPlaybooks(opts.Playbook),
		goansible_playbook.WithPlaybookOptions(playOpts),
	}
	if e.Binary != "" {
		cmdOpts = append(cmdOpts, goansible_playbook.WithBinary(e.Binary))
	}
	cmd := goansible_playbook.NewAnsiblePlaybookCmd(cmdOpts...)
	return cmd.Command()
}
