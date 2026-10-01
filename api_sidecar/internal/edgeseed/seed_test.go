package edgeseed

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSeedMistConfig(t *testing.T) {
	owner := ids{uid: os.Getuid(), gid: os.Getgid()}
	seedJSON := []byte(`{"config":{"controller":{"interface":"0.0.0.0","port":4242},"protocols":[{"connector":"HLS"}]},"streams":{"live":{"source":"push://"}}}` + "\n")

	t.Run("missing runtime config is created from the seed file", func(t *testing.T) {
		dir := t.TempDir()
		conf := filepath.Join(dir, "mistserver.conf")
		seed := filepath.Join(dir, "mistserver.seed.conf")
		if err := os.WriteFile(seed, seedJSON, 0o444); err != nil {
			t.Fatal(err)
		}
		if err := seedMistConfig(conf, seed, owner); err != nil {
			t.Fatalf("seedMistConfig: %v", err)
		}
		out, err := os.ReadFile(conf)
		if err != nil {
			t.Fatalf("runtime config not created: %v", err)
		}
		config, c := controllerOf(t, out)
		if c["interface"] != "127.0.0.1" || c["port"] != float64(4242) {
			t.Fatalf("controller = %v", c)
		}
		if config["protocols"] == nil {
			t.Fatalf("seed protocols not carried into the runtime config: %s", out)
		}
		var data map[string]any
		_ = json.Unmarshal(out, &data)
		if data["streams"] == nil {
			t.Fatalf("seed streams not carried into the runtime config: %s", out)
		}
		info, err := os.Stat(conf)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("runtime config mode = %o, want 600", info.Mode().Perm())
		}
		after, err := os.ReadFile(seed)
		if err != nil || string(after) != string(seedJSON) {
			t.Fatalf("seed file changed: err=%v content=%s", err, after)
		}
	})

	t.Run("seed matching the listener is still copied", func(t *testing.T) {
		dir := t.TempDir()
		conf := filepath.Join(dir, "mistserver.conf")
		seed := filepath.Join(dir, "mistserver.seed.conf")
		in := []byte(`{"config":{"controller":{"interface":"127.0.0.1","port":4242}},"streams":{"a":{}}}` + "\n")
		if err := os.WriteFile(seed, in, 0o444); err != nil {
			t.Fatal(err)
		}
		if err := seedMistConfig(conf, seed, owner); err != nil {
			t.Fatalf("seedMistConfig: %v", err)
		}
		out, err := os.ReadFile(conf)
		if err != nil || string(out) != string(in) {
			t.Fatalf("runtime config = %s, err=%v; want the seed verbatim", out, err)
		}
		if info, _ := os.Stat(conf); info.Mode().Perm() != 0o600 {
			t.Fatalf("runtime config mode = %o, want 600", info.Mode().Perm())
		}
	})

	t.Run("existing runtime config ignores the seed file", func(t *testing.T) {
		dir := t.TempDir()
		conf := filepath.Join(dir, "mistserver.conf")
		seed := filepath.Join(dir, "mistserver.seed.conf")
		existing := []byte(`{"account":{"frameworks":{"password":"digest"}},"config":{"controller":{"interface":"127.0.0.1","port":4242}}}` + "\n")
		if err := os.WriteFile(conf, existing, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(seed, seedJSON, 0o444); err != nil {
			t.Fatal(err)
		}
		if err := seedMistConfig(conf, seed, owner); err != nil {
			t.Fatalf("seedMistConfig: %v", err)
		}
		out, _ := os.ReadFile(conf)
		if string(out) != string(existing) {
			t.Fatalf("runtime config replaced from the seed: %s", out)
		}
	})

	t.Run("existing runtime config never reads the seed path", func(t *testing.T) {
		dir := t.TempDir()
		conf := filepath.Join(dir, "mistserver.conf")
		// A directory at the seed path fails any read of it, so this passes
		// only when the seed is not read at all.
		seed := filepath.Join(dir, "mistserver.seed.conf")
		if err := os.Mkdir(seed, 0o755); err != nil {
			t.Fatal(err)
		}
		existing := []byte(`{"config":{"controller":{"interface":"0.0.0.0","port":4242}}}`)
		if err := os.WriteFile(conf, existing, 0o640); err != nil {
			t.Fatal(err)
		}
		if err := seedMistConfig(conf, seed, owner); err != nil {
			t.Fatalf("seedMistConfig: %v", err)
		}
		_, c := controllerOf(t, mustRead(t, conf))
		if c["interface"] != "127.0.0.1" {
			t.Fatalf("controller listener not reconciled: %v", c)
		}
		if info, _ := os.Stat(conf); info.Mode().Perm() != 0o640 {
			t.Fatalf("existing config mode changed to %o", info.Mode().Perm())
		}
	})

	t.Run("no seed file and no runtime config yields the minimal seed", func(t *testing.T) {
		dir := t.TempDir()
		conf := filepath.Join(dir, "mistserver.conf")
		if err := seedMistConfig(conf, filepath.Join(dir, "mistserver.seed.conf"), owner); err != nil {
			t.Fatalf("seedMistConfig: %v", err)
		}
		out := mustRead(t, conf)
		var data map[string]any
		if err := json.Unmarshal(out, &data); err != nil {
			t.Fatal(err)
		}
		if len(data) != 1 {
			t.Fatalf("minimal seed carries more than config: %s", out)
		}
		if _, c := controllerOf(t, out); c["interface"] != "127.0.0.1" || c["port"] != float64(4242) {
			t.Fatalf("controller = %v", c)
		}
		if info, _ := os.Stat(conf); info.Mode().Perm() != 0o600 {
			t.Fatalf("runtime config mode = %o, want 600", info.Mode().Perm())
		}
	})

	t.Run("unparseable seed file is refused", func(t *testing.T) {
		dir := t.TempDir()
		conf := filepath.Join(dir, "mistserver.conf")
		seed := filepath.Join(dir, "mistserver.seed.conf")
		if err := os.WriteFile(seed, []byte("{not json"), 0o444); err != nil {
			t.Fatal(err)
		}
		if err := seedMistConfig(conf, seed, owner); err == nil {
			t.Fatal("expected an error for an unparseable seed file")
		}
		if _, err := os.Stat(conf); !os.IsNotExist(err) {
			t.Fatalf("runtime config created from an unparseable seed (stat err=%v)", err)
		}
	})
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func controllerOf(t *testing.T, raw []byte) (map[string]any, map[string]any) {
	t.Helper()
	var data map[string]any
	if err := json.Unmarshal(raw, &data); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	config := data["config"].(map[string]any)
	return config, config["controller"].(map[string]any)
}

func TestReconcileMistControllerConfig(t *testing.T) {
	t.Run("empty file gets the seed", func(t *testing.T) {
		out, changed, err := reconcileMistControllerConfig(nil)
		if err != nil || !changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
		if _, c := controllerOf(t, out); c["interface"] != "127.0.0.1" || c["port"] != float64(4242) {
			t.Fatalf("controller = %v", c)
		}
	})

	t.Run("stale listener is fixed and the rest of the config kept", func(t *testing.T) {
		in := []byte(`{"config":{"controller":{"interface":"0.0.0.0","port":4242,"username":"frameworks"},"protocols":[{"connector":"HLS"}]},"streams":{"live":{"source":"push://"}}}`)
		out, changed, err := reconcileMistControllerConfig(in)
		if err != nil || !changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
		config, c := controllerOf(t, out)
		if c["interface"] != "127.0.0.1" || c["username"] != "frameworks" {
			t.Fatalf("controller = %v", c)
		}
		if config["protocols"] == nil {
			t.Fatal("protocols dropped")
		}
		var data map[string]any
		_ = json.Unmarshal(out, &data)
		if data["streams"] == nil {
			t.Fatal("streams dropped")
		}
	})

	t.Run("matching config is left byte for byte", func(t *testing.T) {
		in := []byte(`{"config":{"controller":{"interface":"127.0.0.1","port":4242}},"b":1}` + "\n")
		out, changed, err := reconcileMistControllerConfig(in)
		if err != nil || changed || string(out) != string(in) {
			t.Fatalf("changed=%v err=%v out=%s", changed, err, out)
		}
	})

	t.Run("unparseable config is refused", func(t *testing.T) {
		if _, _, err := reconcileMistControllerConfig([]byte("{not json")); err == nil {
			t.Fatal("expected a parse error")
		}
	})
}
