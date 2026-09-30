package edgeseed

import (
	"encoding/json"
	"testing"
)

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
