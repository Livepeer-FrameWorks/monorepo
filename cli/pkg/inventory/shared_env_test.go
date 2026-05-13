package inventory

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadSharedEnvMergesFilesInOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.env"), "KEY1=one\nKEY2=two\n")
	writeFile(t, filepath.Join(dir, "b.env"), "KEY2=override\nKEY3=three\n")

	m := &Manifest{EnvFiles: []string{"a.env", "b.env"}}
	env, err := LoadSharedEnv(m, dir, "")
	if err != nil {
		t.Fatalf("LoadSharedEnv: %v", err)
	}
	if env["KEY1"] != "one" {
		t.Errorf("KEY1 = %q, want %q", env["KEY1"], "one")
	}
	if env["KEY2"] != "override" {
		t.Errorf("KEY2 = %q, want %q (later file wins)", env["KEY2"], "override")
	}
	if env["KEY3"] != "three" {
		t.Errorf("KEY3 = %q, want %q", env["KEY3"], "three")
	}
}

func TestLoadSharedEnvSkipsCommentsAndBlanks(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.env"), "# leading comment\n\nFOO=bar\n# mid comment\nBAZ=qux\n")

	m := &Manifest{EnvFiles: []string{"a.env"}}
	env, err := LoadSharedEnv(m, dir, "")
	if err != nil {
		t.Fatalf("LoadSharedEnv: %v", err)
	}
	if env["FOO"] != "bar" || env["BAZ"] != "qux" {
		t.Errorf("unexpected env: %+v", env)
	}
	if len(env) != 2 {
		t.Errorf("expected 2 keys, got %d: %+v", len(env), env)
	}
}

func TestLoadSharedEnvRejectsAbsolutePath(t *testing.T) {
	m := &Manifest{EnvFiles: []string{"/etc/passwd"}}
	_, err := LoadSharedEnv(m, "/tmp", "")
	if err == nil {
		t.Fatal("expected error for absolute path")
	}
}

func TestLoadSharedEnvEmptyManifestFiles(t *testing.T) {
	m := &Manifest{}
	env, err := LoadSharedEnv(m, "", "")
	if err != nil {
		t.Fatalf("LoadSharedEnv: %v", err)
	}
	if len(env) != 0 {
		t.Errorf("expected empty map, got %+v", env)
	}
}

func TestLoadSharedEnvNilManifest(t *testing.T) {
	env, err := LoadSharedEnv(nil, "", "")
	if err != nil {
		t.Fatalf("LoadSharedEnv: %v", err)
	}
	if env == nil || len(env) != 0 {
		t.Errorf("expected empty non-nil map, got %v", env)
	}
}

func TestLoadSharedEnvDoesNotValidateKeys(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "only_one.env"), "RANDOM_KEY=x\n")

	m := &Manifest{EnvFiles: []string{"only_one.env"}}
	env, err := LoadSharedEnv(m, dir, "")
	if err != nil {
		t.Fatalf("generic loader must not care about specific keys: %v", err)
	}
	if _, ok := env["SERVICE_TOKEN"]; ok {
		t.Error("SERVICE_TOKEN should not be synthesized by the loader")
	}
	if env["RANDOM_KEY"] != "x" {
		t.Errorf("RANDOM_KEY = %q, want %q", env["RANDOM_KEY"], "x")
	}
}

func TestResolveSharedEnvPlaceholder(t *testing.T) {
	env := map[string]string{"REDIS_CHATWOOT_PASSWORD": "redis secret"}
	got, err := ResolveSharedEnvPlaceholder("${REDIS_CHATWOOT_PASSWORD}", env)
	if err != nil {
		t.Fatalf("ResolveSharedEnvPlaceholder: %v", err)
	}
	if got != "redis secret" {
		t.Fatalf("resolved placeholder = %q, want redis secret", got)
	}
}

func TestResolveSharedEnvPlaceholderLeavesPlainValue(t *testing.T) {
	got, err := ResolveSharedEnvPlaceholder("literal-password", map[string]string{})
	if err != nil {
		t.Fatalf("ResolveSharedEnvPlaceholder: %v", err)
	}
	if got != "literal-password" {
		t.Fatalf("plain value = %q, want literal-password", got)
	}
}

func TestResolveSharedEnvPlaceholderRejectsMissingKey(t *testing.T) {
	if _, err := ResolveSharedEnvPlaceholder("${MISSING}", map[string]string{}); err == nil {
		t.Fatal("expected missing placeholder key to fail")
	}
}

func TestLoadClusterEnvsKeyedByClusterID(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "eu.env"), "STORAGE_S3_ACCESS_KEY=hetzner-key\nSTORAGE_S3_ENDPOINT=https://nbg1.example\n")
	writeFile(t, filepath.Join(dir, "us.env"), "STORAGE_S3_ACCESS_KEY=r2-key\nSTORAGE_S3_ENDPOINT=https://r2.example\n")

	m := &Manifest{Clusters: map[string]ClusterConfig{
		"media-eu-1": {Name: "EU", EnvFiles: []string{"eu.env"}},
		"media-us-1": {Name: "US", EnvFiles: []string{"us.env"}},
		"core":       {Name: "Core"}, // no env_files — must be omitted from result
	}}

	envs, err := LoadClusterEnvs(m, dir, "")
	if err != nil {
		t.Fatalf("LoadClusterEnvs: %v", err)
	}
	if _, ok := envs["core"]; ok {
		t.Errorf("clusters with no env_files must be omitted, got entry for core")
	}
	if envs["media-eu-1"]["STORAGE_S3_ACCESS_KEY"] != "hetzner-key" {
		t.Errorf("EU access key = %q, want hetzner-key", envs["media-eu-1"]["STORAGE_S3_ACCESS_KEY"])
	}
	if envs["media-us-1"]["STORAGE_S3_ACCESS_KEY"] != "r2-key" {
		t.Errorf("US access key = %q, want r2-key", envs["media-us-1"]["STORAGE_S3_ACCESS_KEY"])
	}
	// Per-cluster envs never spill across clusters.
	if envs["media-eu-1"]["STORAGE_S3_ENDPOINT"] == envs["media-us-1"]["STORAGE_S3_ENDPOINT"] {
		t.Errorf("EU and US endpoints must differ; got identical %q", envs["media-eu-1"]["STORAGE_S3_ENDPOINT"])
	}
}

func TestLoadClusterEnvsMergesFilesInOrder(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "base.env"), "REGION=eu\nBUCKET=default\n")
	writeFile(t, filepath.Join(dir, "override.env"), "BUCKET=override\nEXTRA=more\n")

	m := &Manifest{Clusters: map[string]ClusterConfig{
		"media-eu-1": {EnvFiles: []string{"base.env", "override.env"}},
	}}
	envs, err := LoadClusterEnvs(m, dir, "")
	if err != nil {
		t.Fatalf("LoadClusterEnvs: %v", err)
	}
	got := envs["media-eu-1"]
	if got["REGION"] != "eu" || got["BUCKET"] != "override" || got["EXTRA"] != "more" {
		t.Errorf("merge order broken: %+v", got)
	}
}

func TestLoadClusterEnvsRejectsAbsolutePath(t *testing.T) {
	m := &Manifest{Clusters: map[string]ClusterConfig{
		"media-eu-1": {EnvFiles: []string{"/etc/passwd"}},
	}}
	if _, err := LoadClusterEnvs(m, "/tmp", ""); err == nil {
		t.Fatal("expected error for absolute path")
	}
}

func TestLoadClusterEnvsNilManifest(t *testing.T) {
	envs, err := LoadClusterEnvs(nil, "", "")
	if err != nil {
		t.Fatalf("LoadClusterEnvs: %v", err)
	}
	if len(envs) != 0 {
		t.Errorf("expected empty map, got %+v", envs)
	}
}

func TestLoadClusterEnvsEmptyClusters(t *testing.T) {
	m := &Manifest{}
	envs, err := LoadClusterEnvs(m, "", "")
	if err != nil {
		t.Fatalf("LoadClusterEnvs: %v", err)
	}
	if len(envs) != 0 {
		t.Errorf("expected empty map, got %+v", envs)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
