package cmd

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"frameworks/cli/pkg/backup"
	"frameworks/cli/pkg/provisioner"
	"frameworks/cli/pkg/ssh"
)

type restoreFenceRunner struct {
	commands []string
	err      error
}

func (r *restoreFenceRunner) RunStream(_ context.Context, command string, _ io.Reader, _ io.Writer) (*ssh.CommandResult, error) {
	r.commands = append(r.commands, command)
	return &ssh.CommandResult{}, r.err
}

func TestManifestRestoreFencesInactiveAuthorityCopies(t *testing.T) {
	for _, suffix := range []string{provisioner.RestoreShadowSuffix, provisioner.RestorePreviousSuffix} {
		for _, engine := range []string{backup.EnginePostgres, backup.EngineYugabyte} {
			t.Run(engine+suffix, func(t *testing.T) {
				runner := &restoreFenceRunner{}
				entry := backup.Database{Name: "foghorn_eu", Source: "foghorn"}
				database := entry.Name + suffix
				server := provisioner.DatabaseServer{Engine: engine, Port: 5432}
				if err := fenceRestoredAuthority(context.Background(), runner, server, entry, database); err != nil {
					t.Fatal(err)
				}
				if len(runner.commands) != 1 || !strings.Contains(runner.commands[0], database) || !strings.Contains(runner.commands[0], "INSERT INTO foghorn.media_authority_restore_fence") {
					t.Fatalf("incorrect inactive-copy fence: %v", runner.commands)
				}
				runner.err = errors.New("missing fence table")
				if err := fenceRestoredAuthority(context.Background(), runner, server, entry, database); err == nil {
					t.Fatal("failed fence accepted")
				}
			})
		}
	}
	for _, entry := range []backup.Database{{Name: "purser"}, {Name: "foghorn", Instance: "external"}} {
		runner := &restoreFenceRunner{}
		if err := fenceRestoredAuthority(context.Background(), runner, provisioner.DatabaseServer{}, entry, entry.Name+provisioner.RestoreShadowSuffix); err != nil || len(runner.commands) != 0 {
			t.Fatalf("unrelated database fenced: %+v, %v", entry, err)
		}
	}
}
