package main

import (
	"errors"
	"flag"
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

	base := flag.String("base", filepath.Join(root, "config", "env", "base.env"), "path to base env file")
	secrets := flag.String("secrets", filepath.Join(root, "config", "env", "secrets.env"), "path to secrets env file")
	overlay := flag.String("overlay", "", "overlay env file merged on top of base (last-write-wins)")
	output := flag.String("output", filepath.Join(root, ".env"), "output env file path")
	context := flag.String("context", "dev", "generation context")
	frontendOnly := flag.Bool("frontend-only", false, "emit frontend build env only")
	flag.Parse()

	var overlays []string
	if *overlay != "" {
		overlays = []string{*overlay}
	}

	opts := configgen.Options{
		BaseFile:     *base,
		OverlayFiles: overlays,
		SecretsFile:  *secrets,
		OutputFile:   *output,
		Context:      *context,
		FrontendOnly: *frontendOnly,
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
