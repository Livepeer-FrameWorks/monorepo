package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"frameworks/api_gateway/internal/appconfig"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"github.com/vektah/gqlparser/v2/ast"
)

// The streamingConfig and Signalman dial getters must observe an env-file
// reload; values copied at startup would not.
func TestResolverConfigGettersFollowReload(t *testing.T) {
	env := map[string]string{
		"SERVICE_TOKEN":           "service-token",
		"JWT_SECRET":              "jwt-secret",
		"COMMODORE_GRPC_ADDR":     "commodore:19001",
		"PERISCOPE_GRPC_ADDR":     "periscope:19004",
		"PURSER_GRPC_ADDR":        "purser:19003",
		"QUARTERMASTER_GRPC_ADDR": "quartermaster:19002",
		"SIGNALMAN_GRPC_ADDR":     "signalman:19005",
		"DECKLOG_GRPC_ADDR":       "decklog:18006",
	}
	opts := config.Options{Service: "bridge", Lookup: func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}}
	cfg, err := config.Load[appconfig.Bridge](opts)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	live := config.NewLive(cfg, opts)
	rc := resolverConfig(live)

	if got := rc.Streaming(); got.SRTPort != 8889 || got.RTMPPort != 1935 || got.RootDomain != "" {
		t.Fatalf("default streaming settings = %+v", got)
	}
	if got := rc.SignalmanDial; got.OpenTimeout != 5*time.Second || got.AllowInsecure {
		t.Fatalf("default dial settings = %+v", got)
	}

	env["STREAMING_SRT_PORT"] = "9999"
	env["BRAND_DOMAIN"] = "example.net"
	if err := live.Reload(); err != nil {
		t.Fatalf("reload: %v", err)
	}

	if got := rc.Streaming(); got.SRTPort != 9999 || got.RootDomain != "example.net" {
		t.Fatalf("streaming settings after reload = %+v", got)
	}
	if rc.SignalmanAddr != "signalman:19005" || rc.MaxSubscriptionsPerTenant != 100 {
		t.Fatalf("startup values = addr %q max %d", rc.SignalmanAddr, rc.MaxSubscriptionsPerTenant)
	}
}

func TestIsIntrospectionOperationAllFields(t *testing.T) {
	op := &ast.OperationDefinition{
		SelectionSet: ast.SelectionSet{
			&ast.Field{Name: "__schema"},
			&ast.Field{Name: "__type"},
		},
	}

	if !isIntrospectionOperation(op) {
		t.Fatal("expected introspection operation to be recognized")
	}
}

func TestIsIntrospectionOperationMixedFields(t *testing.T) {
	op := &ast.OperationDefinition{
		SelectionSet: ast.SelectionSet{
			&ast.Field{Name: "__schema"},
			&ast.Field{Name: "streamsConnection"},
		},
	}

	if isIntrospectionOperation(op) {
		t.Fatal("expected mixed fields to disable introspection bypass")
	}
}

func TestIsIntrospectionOperationInlineFragment(t *testing.T) {
	op := &ast.OperationDefinition{
		SelectionSet: ast.SelectionSet{
			&ast.InlineFragment{
				SelectionSet: ast.SelectionSet{
					&ast.Field{Name: "__schema"},
				},
			},
		},
	}

	if isIntrospectionOperation(op) {
		t.Fatal("expected inline fragments to disable introspection bypass")
	}
}

func TestLoadSkillFilesFromWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	writeSkillFile(t, dir, "SKILL.md", "skill")
	writeSkillFile(t, dir, "skill.json", `{"name":"frameworks"}`)

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})
	files := loadSkillFiles(logging.NewLoggerWithService("bridge-test"), "", "")
	if string(files.skillMD) != "skill" {
		t.Fatalf("skillMD = %q, want %q", files.skillMD, "skill")
	}
	if string(files.skillJSON) != `{"name":"frameworks"}` {
		t.Fatalf("skillJSON = %q, want %q", files.skillJSON, `{"name":"frameworks"}`)
	}
}

func TestLoadSkillFilesFromRepoRootWhenRunFromModule(t *testing.T) {
	root := t.TempDir()
	moduleDir := filepath.Join(root, "api_gateway")
	skillsDir := filepath.Join(root, "docs", "skills")
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		t.Fatalf("create module dir: %v", err)
	}
	writeSkillFile(t, skillsDir, "SKILL.md", "module skill")
	writeSkillFile(t, skillsDir, "skill.json", `{"name":"module"}`)

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(moduleDir); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Fatalf("restore working directory: %v", err)
		}
	})
	files := loadSkillFiles(logging.NewLoggerWithService("bridge-test"), "", "")
	if string(files.skillMD) != "module skill" {
		t.Fatalf("skillMD = %q, want %q", files.skillMD, "module skill")
	}
	if string(files.skillJSON) != `{"name":"module"}` {
		t.Fatalf("skillJSON = %q, want %q", files.skillJSON, `{"name":"module"}`)
	}
}

func TestLoadSkillFilesPrefersConfiguredDirectoryAndWallet(t *testing.T) {
	configured := t.TempDir()
	writeSkillFile(t, configured, "SKILL.md", "configured skill")
	writeSkillFile(t, configured, "skill.json", `{"name":"configured"}`)
	writeSkillFile(t, configured, "did.json", `{"id":"did:web:example","wallet":"{{X402_GAS_WALLET_ADDRESS}}"}`)

	files := loadSkillFiles(logging.NewLoggerWithService("bridge-test"), configured, "0xabc")
	if string(files.skillMD) != "configured skill" {
		t.Fatalf("skillMD = %q, want the configured directory's file", files.skillMD)
	}
	if string(files.didJSON) != `{"id":"did:web:example","wallet":"0xabc"}` {
		t.Fatalf("didJSON = %q, want the wallet substituted", files.didJSON)
	}
}

func writeSkillFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create skill dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill file: %v", err)
	}
}
