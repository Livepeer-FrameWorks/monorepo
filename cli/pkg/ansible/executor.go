package ansible

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Executor runs Ansible playbooks
type Executor struct {
	ansiblePath string
	workDir     string
}

// NewExecutor creates a new Ansible executor
func NewExecutor(workDir string) (*Executor, error) {
	// Find ansible-playbook binary
	ansiblePath, err := exec.LookPath("ansible-playbook")
	if err != nil {
		return nil, fmt.Errorf("ansible-playbook not found in PATH: %w", err)
	}

	return &Executor{
		ansiblePath: ansiblePath,
		workDir:     workDir,
	}, nil
}

// Execute runs an Ansible playbook
func (e *Executor) Execute(ctx context.Context, opts ExecuteOptions) (*ExecuteResult, error) {
	result := &ExecuteResult{
		PlaybookRun: &PlaybookRunStats{},
	}

	// Build command args
	args := []string{opts.Playbook}

	if opts.Inventory != "" {
		args = append(args, "-i", opts.Inventory)
	}

	if opts.Verbose {
		args = append(args, "-vvv")
	}

	if opts.Check {
		args = append(args, "--check")
	}

	if opts.Diff {
		args = append(args, "--diff")
	}

	if opts.Limit != "" {
		args = append(args, "--limit", opts.Limit)
	}

	if opts.BecomeUser != "" {
		args = append(args, "--become-user", opts.BecomeUser)
	}

	if opts.User != "" {
		args = append(args, "--user", opts.User)
	}

	if opts.PrivateKey != "" {
		args = append(args, "--private-key", opts.PrivateKey)
	}

	if len(opts.Tags) > 0 {
		args = append(args, "--tags", strings.Join(opts.Tags, ","))
	}

	if len(opts.SkipTags) > 0 {
		args = append(args, "--skip-tags", strings.Join(opts.SkipTags, ","))
	}

	// Add extra vars
	for key, value := range opts.ExtraVars {
		args = append(args, "-e", fmt.Sprintf("%s=%s", key, value))
	}

	// Create command
	cmd := exec.CommandContext(ctx, e.ansiblePath, args...)
	if e.workDir != "" {
		cmd.Dir = e.workDir
	}

	// Capture combined output
	output, err := cmd.CombinedOutput()
	result.Output = string(output)

	if err != nil {
		result.Success = false
		result.Error = err
		return result, err
	}

	// Parse playbook stats from output
	result.PlaybookRun = parsePlaybookStats(result.Output)
	result.Success = result.PlaybookRun.Failures == 0 && result.PlaybookRun.Unreachable == 0

	return result, nil
}

// ExecutePlaybook generates temp files and executes a playbook
func (e *Executor) ExecutePlaybook(ctx context.Context, playbook *Playbook, inventory *Inventory, opts ExecuteOptions) (*ExecuteResult, error) {
	// Create temp directory for playbook and inventory
	tmpDir, err := os.MkdirTemp("", "frameworks-ansible-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Write playbook to temp file
	playbookPath := filepath.Join(tmpDir, "playbook.yml")
	playbookYAML, err := playbook.ToYAML()
	if err != nil {
		return nil, fmt.Errorf("failed to generate playbook YAML: %w", err)
	}

	if err := os.WriteFile(playbookPath, playbookYAML, 0644); err != nil {
		return nil, fmt.Errorf("failed to write playbook: %w", err)
	}

	// Write inventory to temp file
	inventoryPath := filepath.Join(tmpDir, "inventory.ini")
	inventoryINI := inventory.ToINI()

	if err := os.WriteFile(inventoryPath, []byte(inventoryINI), 0644); err != nil {
		return nil, fmt.Errorf("failed to write inventory: %w", err)
	}

	// Update options with temp paths
	opts.Playbook = playbookPath
	opts.Inventory = inventoryPath

	// Execute
	return e.Execute(ctx, opts)
}

// parsePlaybookStats extracts playbook run statistics from output
func parsePlaybookStats(output string) *PlaybookRunStats {
	stats := &PlaybookRunStats{}

	// Look for PLAY RECAP section
	scanner := bufio.NewScanner(strings.NewReader(output))
	inRecap := false

	for scanner.Scan() {
		line := scanner.Text()

		if strings.Contains(line, "PLAY RECAP") {
			inRecap = true
			continue
		}

		if !inRecap {
			continue
		}

		// Parse stats line: hostname : ok=2 changed=1 unreachable=0 failed=0 skipped=0
		if matches := regexp.MustCompile(`ok=(\d+)`).FindStringSubmatch(line); len(matches) > 1 {
			if val, err := strconv.Atoi(matches[1]); err == nil {
				stats.Ok += val
			}
		}

		if matches := regexp.MustCompile(`changed=(\d+)`).FindStringSubmatch(line); len(matches) > 1 {
			if val, err := strconv.Atoi(matches[1]); err == nil {
				stats.Changed += val
			}
		}

		if matches := regexp.MustCompile(`unreachable=(\d+)`).FindStringSubmatch(line); len(matches) > 1 {
			if val, err := strconv.Atoi(matches[1]); err == nil {
				stats.Unreachable += val
			}
		}

		if matches := regexp.MustCompile(`failed=(\d+)`).FindStringSubmatch(line); len(matches) > 1 {
			if val, err := strconv.Atoi(matches[1]); err == nil {
				stats.Failures += val
			}
		}

		if matches := regexp.MustCompile(`skipped=(\d+)`).FindStringSubmatch(line); len(matches) > 1 {
			if val, err := strconv.Atoi(matches[1]); err == nil {
				stats.Skipped += val
			}
		}
	}

	return stats
}

// CheckAnsibleInstalled verifies Ansible is installed
func CheckAnsibleInstalled() error {
	_, err := exec.LookPath("ansible-playbook")
	if err != nil {
		return fmt.Errorf("ansible-playbook not found in PATH - please install Ansible: %w", err)
	}
	return nil
}

// GetAnsibleVersion returns the installed Ansible version
func GetAnsibleVersion() (string, error) {
	cmd := exec.Command("ansible-playbook", "--version")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	// Parse version from first line
	lines := strings.Split(string(output), "\n")
	if len(lines) > 0 {
		// Extract version number
		if matches := regexp.MustCompile(`ansible-playbook \[core ([\d.]+)\]`).FindStringSubmatch(lines[0]); len(matches) > 1 {
			return matches[1], nil
		}
		// Fallback for older versions
		if matches := regexp.MustCompile(`ansible-playbook ([\d.]+)`).FindStringSubmatch(lines[0]); len(matches) > 1 {
			return matches[1], nil
		}
	}

	return "", fmt.Errorf("could not parse ansible version from output")
}
