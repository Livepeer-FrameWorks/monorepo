//go:build darwin

package credentials

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func defaultStore() Store { return keychainStore{} }

type keychainStore struct{}

func (keychainStore) Name() string { return "keychain" }

const keychainTimeout = 10 * time.Second

func (keychainStore) Get(account string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), keychainTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", "find-generic-password",
		"-s", ServiceName,
		"-a", account,
		"-w",
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if isNotFound(stderr.String(), err) {
			return "", nil
		}
		return "", fmt.Errorf("keychain get %s/%s: %w: %s", ServiceName, account, err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimRight(stdout.String(), "\n"), nil
}

func (keychainStore) Set(account, value string) error {
	ctx, cancel := context.WithTimeout(context.Background(), keychainTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", "add-generic-password",
		"-s", ServiceName,
		"-a", account,
		"-w", value,
		"-U",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain set %s/%s: %w: %s", ServiceName, account, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (keychainStore) Delete(account string) error {
	ctx, cancel := context.WithTimeout(context.Background(), keychainTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "security", "delete-generic-password",
		"-s", ServiceName,
		"-a", account,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if isNotFound(stderr.String(), err) {
			return nil
		}
		return fmt.Errorf("keychain delete %s/%s: %w: %s", ServiceName, account, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// isNotFound — macOS `security` writes "could not be found" and exits 44
// when an item is missing.
func isNotFound(stderr string, err error) bool {
	if strings.Contains(stderr, "could not be found") {
		return true
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 44 {
		return true
	}
	return false
}
