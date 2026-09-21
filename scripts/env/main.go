package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/configgen"
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

	env, err := configgen.Generate(opts)
	if err != nil {
		return err
	}
	if !*frontendOnly {
		if err := appendComposeProfiles(opts.OutputFile, env); err != nil {
			return err
		}
	}

	fmt.Printf("wrote %s\n", opts.OutputFile)
	return nil
}

// composeProfilePresets are the docker-compose.yml profile combinations the dev
// stack supports. make verify-compose-profiles validates every entry it reads
// from the generated file.
const composeProfilePresets = `
# Docker Compose profiles for the dev stack. Presets:
#   COMPOSE_PROFILES=                control plane only
#   COMPOSE_PROFILES=edge            control plane and the media edge
#   COMPOSE_PROFILES=edge,support    plus Chatwoot and Listmonk
#   COMPOSE_PROFILES=edge,two-cell   plus a second media cell
#   COMPOSE_PROFILES=llm             control plane and local Ollama
`

// appendComposeProfiles writes the preset list and, unless the merged env files
// already set COMPOSE_PROFILES, the default edge profile.
func appendComposeProfiles(path string, env map[string]string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	block := composeProfilePresets
	if _, set := env["COMPOSE_PROFILES"]; !set {
		block += "COMPOSE_PROFILES=\"edge\"\n"
	}
	if _, err := f.WriteString(block); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
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
