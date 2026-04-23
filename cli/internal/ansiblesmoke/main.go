// Smoke test for the ansiblerun Phase-1 wiring.
//
// Exercises: CollectionEnsurer (with --cache-dir override), InventoryRenderer
// (against localhost), and Executor.Execute against the frameworks.infra.hello
// role. Used by `make provision-hello` — not a user-facing CLI entrypoint.
//
// Run:
//
//	go run ./internal/ansiblesmoke \
//	    -requirements ../ansible/requirements.yml \
//	    -playbook ../ansible/playbooks/hello.yml
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"frameworks/cli/pkg/ansiblerun"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("ansiblesmoke: %v", err)
	}
}

func run() error {
	var (
		requirementsFlag = flag.String("requirements", "", "path to ansible requirements.yml (required)")
		playbookFlag     = flag.String("playbook", "", "path to playbook YAML (required)")
		workDirFlag      = flag.String("workdir", "", "ansible-playbook cwd (default: parent of playbook)")
		cacheDirFlag     = flag.String("cache-dir", "", "collection cache override (default: $XDG_CACHE_HOME/frameworks/ansible-collections)")
		greetingFlag     = flag.String("greeting", "FrameWorks CLI wiring is live", "extra var passed to the hello role")
		verboseFlag      = flag.Int("v", 0, "ansible-playbook verbosity 0..4")
	)
	flag.Parse()

	if *requirementsFlag == "" || *playbookFlag == "" {
		flag.Usage()
		return fmt.Errorf("-requirements and -playbook are required")
	}

	reqAbs, err := filepath.Abs(*requirementsFlag)
	if err != nil {
		return fmt.Errorf("resolve requirements: %w", err)
	}
	playAbs, err := filepath.Abs(*playbookFlag)
	if err != nil {
		return fmt.Errorf("resolve playbook: %w", err)
	}
	workDir := *workDirFlag
	if workDir == "" {
		workDir = filepath.Dir(playAbs)
		if filepath.Base(workDir) == "playbooks" {
			workDir = filepath.Dir(workDir)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Println("==> installing Ansible collections + roles")
	ensurer := &ansiblerun.CollectionEnsurer{
		RequirementsFile: reqAbs,
		CacheDir:         *cacheDirFlag,
	}
	cache, err := ensurer.Ensure(ctx)
	if err != nil {
		return fmt.Errorf("ensure collections: %w", err)
	}
	fmt.Printf("    collections: %s\n", cache.CollectionsPath)
	fmt.Printf("    roles:       %s\n", cache.RolesPath)

	fmt.Println("==> rendering inventory (localhost)")
	invDir, err := os.MkdirTemp("", "frameworks-ansiblesmoke-*")
	if err != nil {
		return fmt.Errorf("mkdtemp: %w", err)
	}
	defer os.RemoveAll(invDir)

	renderer := &ansiblerun.InventoryRenderer{}
	invPath, err := renderer.Render(invDir, ansiblerun.Inventory{
		Hosts: []ansiblerun.Host{
			{
				Name:       "localhost",
				Address:    "127.0.0.1",
				Connection: "local",
			},
		},
	})
	if err != nil {
		return fmt.Errorf("render inventory: %w", err)
	}
	fmt.Printf("    inventory: %s\n", invPath)

	fmt.Println("==> running ansible-playbook")
	exec, err := ansiblerun.NewExecutor()
	if err != nil {
		return err
	}

	// Let ansible.cfg (workDir/ansible.cfg) resolve collections_path —
	// explicitly exporting ANSIBLE_COLLECTIONS_PATH here would clobber the
	// source-first ordering (./collections then ./.cache/collections).
	_ = cache
	envVars := map[string]string{}
	for _, name := range []string{"SOPS_AGE_KEY_FILE", "SOPS_AGE_KEY", "HOME", "USER", "PATH"} {
		if v := os.Getenv(name); v != "" {
			envVars[name] = v
		}
	}

	return exec.Execute(ctx, ansiblerun.ExecuteOptions{
		Playbook:  playAbs,
		Inventory: invPath,
		ExtraVars: map[string]any{
			"hello_greeting": *greetingFlag,
		},
		Verbose: *verboseFlag,
		WorkDir: workDir,
		EnvVars: envVars,
	})
}
