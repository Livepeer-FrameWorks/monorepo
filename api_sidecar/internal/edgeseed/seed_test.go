package edgeseed

import (
	"encoding/json"
	"strings"
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

// md5("secret"), the account digest Mist stores for password "secret".
const secretDigest = "5ebe2294ecd0e0f08eab7690d2a6ee69"

func TestReconcileMistAccount(t *testing.T) {
	t.Run("account is seeded and the rest of the config kept", func(t *testing.T) {
		in := []byte(`{"account":{"ops":{"password":"x"}},"config":{"controller":{"interface":"127.0.0.1","port":4242}},"streams":{"live":{"source":"push://"}},"big":9007199254740993}`)
		out, changed, err := reconcileMistAccount(in, "frameworks", "secret")
		if err != nil || !changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
		var data map[string]any
		if err := json.Unmarshal(out, &data); err != nil {
			t.Fatal(err)
		}
		accounts := data["account"].(map[string]any)
		if got := accounts["frameworks"].(map[string]any)["password"]; got != secretDigest {
			t.Fatalf("frameworks password = %v", got)
		}
		if accounts["ops"] == nil || data["streams"] == nil || data["config"] == nil {
			t.Fatalf("existing keys dropped: %s", out)
		}
		if !strings.Contains(string(out), `"big":9007199254740993`) {
			t.Fatalf("large integer not kept exactly: %s", out)
		}
		if strings.Contains(string(out), "secret") {
			t.Fatalf("plaintext password written: %s", out)
		}
	})

	t.Run("a rotated password replaces the stored digest", func(t *testing.T) {
		in := []byte(`{"account":{"frameworks":{"password":"0123"}}}`)
		out, changed, err := reconcileMistAccount(in, "frameworks", "secret")
		if err != nil || !changed || !strings.Contains(string(out), secretDigest) || strings.Contains(string(out), "0123") {
			t.Fatalf("changed=%v err=%v out=%s", changed, err, out)
		}
	})

	t.Run("matching account is left byte for byte", func(t *testing.T) {
		in := []byte(`{"account":{"frameworks":{"password":"` + secretDigest + `"}},"b":1}` + "\n")
		out, changed, err := reconcileMistAccount(in, "frameworks", "secret")
		if err != nil || changed || string(out) != string(in) {
			t.Fatalf("changed=%v err=%v out=%s", changed, err, out)
		}
	})

	t.Run("empty password leaves the config alone", func(t *testing.T) {
		in := []byte(`{not json`)
		out, changed, err := reconcileMistAccount(in, "frameworks", "")
		if err != nil || changed || string(out) != string(in) {
			t.Fatalf("changed=%v err=%v out=%s", changed, err, out)
		}
	})

	t.Run("unparseable config is refused", func(t *testing.T) {
		if _, _, err := reconcileMistAccount([]byte("{not json"), "frameworks", "secret"); err == nil {
			t.Fatal("expected a parse error")
		}
	})

	t.Run("empty config yields the account alone", func(t *testing.T) {
		out, changed, err := reconcileMistAccount(nil, "frameworks", "secret")
		if err != nil || !changed || string(out) != `{"account":{"frameworks":{"password":"`+secretDigest+`"}}}`+"\n" {
			t.Fatalf("changed=%v err=%v out=%s", changed, err, out)
		}
	})
}

func TestMistAccountFromEnvDefaultsUsername(t *testing.T) {
	t.Setenv("MIST_API_USERNAME", " ")
	t.Setenv("MIST_API_PASSWORD", "secret")
	user, password := mistAccountFromEnv()
	if user != "frameworks" || password != "secret" {
		t.Fatalf("user=%q password set=%v", user, password != "")
	}
}
