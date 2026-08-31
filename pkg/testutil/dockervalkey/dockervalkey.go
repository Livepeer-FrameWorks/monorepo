// Package dockervalkey provides a real, release-pinned Valkey contract harness.
package dockervalkey

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

const readyBudget = 45 * time.Second

// Image returns the exact Valkey image pinned for the current release.
func Image() (string, error) { return pinnedImage("valkey") }

// Instance owns one persistent Valkey container and its topology-agnostic client.
type Instance struct {
	Name   string
	Volume string
	Image  string
	Addr   string
	Client goredis.UniversalClient
}

// Start launches the exact Valkey image pinned for the release. AOF is enabled so
// Stop/Start can prove reconnect and persistence behavior rather than only memory state.
func Start(t testing.TB) *Instance {
	t.Helper()
	image, err := Image()
	if err != nil {
		t.Fatalf("resolve Valkey image: %v", err)
	}
	name := fmt.Sprintf("fw-valkey-contract-%d", time.Now().UnixNano())
	volume := name + "-data"
	if out, createErr := dockerpg.CLI("volume", "create", volume); createErr != nil {
		t.Fatalf("create Valkey volume: %v\n%s", createErr, out)
	}
	instance := &Instance{Name: name, Volume: volume, Image: image}
	t.Cleanup(func() {
		if instance.Client != nil {
			_ = instance.Client.Close()
		}
		bestEffortCLI("rm", "-f", name)
		bestEffortCLI("volume", "rm", "-f", volume)
	})
	instance.launch(t)
	instance.waitReady(t)
	return instance
}

// Restart replaces the process and container over the same explicit data volume. Recreating the
// container is intentional: Docker Desktop can leave a stopped/started container's published port
// permanently refused even while Valkey is healthy inside it.
func (i *Instance) Restart(t testing.TB) {
	t.Helper()
	i.Stop(t)
	i.ReplaceStopped(t)
}

// Stop makes the engine unavailable while retaining its explicit data volume.
func (i *Instance) Stop(t testing.TB) {
	t.Helper()
	if out, err := dockerpg.CLI("stop", "-t", "5", i.Name); err != nil {
		t.Fatalf("stop Valkey: %v\n%s", err, out)
	}
}

// ReplaceStopped recreates a stopped container over the retained data volume and publishes a fresh port.
func (i *Instance) ReplaceStopped(t testing.TB) {
	t.Helper()
	if i.Client != nil {
		_ = i.Client.Close()
	}
	if out, err := dockerpg.CLI("rm", "-f", i.Name); err != nil {
		t.Fatalf("remove stopped Valkey: %v\n%s", err, out)
	}
	i.launch(t)
	i.waitReady(t)
}

func (i *Instance) launch(t testing.TB) {
	t.Helper()
	if out, err := dockerpg.Run("run", "-d", "--name", i.Name, "-P", "-v", i.Volume+":/data", i.Image,
		"valkey-server", "--appendonly", "yes", "--appendfsync", "always"); err != nil {
		t.Fatalf("start Valkey: %v\n%s", err, out)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(i.Name, "6379/tcp")
	if err != nil {
		t.Fatal(err)
	}
	i.Addr = "127.0.0.1:" + port
	i.Client = goredis.NewUniversalClient(&goredis.UniversalOptions{
		Addrs:        []string{i.Addr},
		DialTimeout:  3 * time.Second,
		ReadTimeout:  3 * time.Second,
		WriteTimeout: 3 * time.Second,
	})
}

func (i *Instance) waitReady(t testing.TB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), readyBudget)
	defer cancel()
	var last error
	for ctx.Err() == nil {
		probe, pcancel := context.WithTimeout(ctx, 2*time.Second)
		last = i.Client.Ping(probe).Err()
		pcancel()
		if last == nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	logs := bestEffortCLI("logs", "--tail", "40", i.Name)
	t.Fatalf("Valkey not ready within %s: %v\nlogs:\n%s", readyBudget, last, logs)
}

func bestEffortCLI(args ...string) string {
	out, err := dockerpg.CLI(args...)
	if err != nil {
		return fmt.Sprintf("%s\n(docker %s failed: %v)", out, strings.Join(args, " "), err)
	}
	return out
}

func pinnedImage(name string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for dir, depth := wd, 0; depth < 10; depth++ {
		path := filepath.Join(dir, "config", "infrastructure.yaml")
		if data, readErr := os.ReadFile(path); readErr == nil {
			active, image, digest := false, "", ""
			for line := range strings.SplitSeq(string(data), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "- name:") {
					if active {
						break
					}
					active = strings.TrimSpace(strings.TrimPrefix(trimmed, "- name:")) == name
					continue
				}
				if active && strings.HasPrefix(trimmed, "image:") {
					image = strings.TrimSpace(strings.TrimPrefix(trimmed, "image:"))
				}
				if active && strings.HasPrefix(trimmed, "digest:") {
					digest = strings.TrimSpace(strings.TrimPrefix(trimmed, "digest:"))
				}
			}
			if image == "" || digest == "" {
				return "", fmt.Errorf("infrastructure/%s must declare image and digest", name)
			}
			return image + "@" + digest, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("config/infrastructure.yaml not found from test working directory")
}
