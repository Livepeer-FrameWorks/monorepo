package sops

import (
	"fmt"
	"os"
	"path/filepath"
)

func resolveAgeKeyFile(ageKeyFile string) (string, error) {
	abs, err := filepath.Abs(ageKeyFile)
	if err != nil {
		return "", fmt.Errorf("resolve age key path: %w", err)
	}
	if _, err := os.Stat(abs); err != nil {
		return "", fmt.Errorf("age key file not found: %s", abs)
	}
	return abs, nil
}
