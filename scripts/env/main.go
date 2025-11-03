package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"frameworks/pkg/configgen"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "env: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	root, err := findRepoRoot()
	if err != nil {
		return err
	}

	opts := configgen.Options{
		BaseFile:    filepath.Join(root, "config", "env", "base.env"),
		SecretsFile: filepath.Join(root, "config", "env", "secrets.env"),
		OutputFile:  filepath.Join(root, ".env"),
		Context:     "dev",
	}

	if _, err := configgen.Generate(opts); err != nil {
		return err
	}

	fmt.Printf("wrote %s\n", opts.OutputFile)
	return nil
}

func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("could not locate repository root (missing .git)")
		}
		dir = parent
	}
}
