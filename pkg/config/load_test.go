package config

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	logtest "github.com/sirupsen/logrus/hooks/test"
)

type SampleBlock struct {
	Token string `env:"SAMPLE_TOKEN" required:"true" secret:"true" desc:"Token required by the sample block." introduced:"v0.3.0"`
}

type sampleConfig struct {
	SampleBlock
	Name    string        `env:"SAMPLE_NAME" required:"true" desc:"Name required by the sample config." introduced:"v0.3.0"`
	Mode    string        `env:"SAMPLE_MODE" default:"debug" desc:"Mode with a literal default value." introduced:"v0.3.0"`
	Enabled bool          `env:"SAMPLE_ENABLED" default:"false" desc:"Boolean toggle parsed strictly." introduced:"v0.3.0"`
	Limit   int           `env:"SAMPLE_LIMIT" default:"10" desc:"Integer limit parsed strictly." introduced:"v0.3.0"`
	Timeout time.Duration `env:"SAMPLE_TIMEOUT" default:"5s" desc:"Duration parsed with time.ParseDuration." introduced:"v0.3.0"`
	Origins []string      `env:"SAMPLE_ORIGINS" desc:"Comma separated list with trimming." introduced:"v0.3.0"`
	Port    string        `env:"SAMPLE_PORT" default:"@servicedefs.http_port" desc:"Port resolved from servicedefs." introduced:"v0.3.0"`
	GRPC    string        `env:"SAMPLE_GRPC_PORT" default:"@servicedefs.grpc_port" desc:"gRPC port resolved from servicedefs." introduced:"v0.3.0"`
	Old     string        `env:"SAMPLE_OLD" deprecated:"v0.3.0" replacement:"SAMPLE_NAME" desc:"Deprecated alias kept for compatibility." introduced:"v0.3.0"`
}

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := values[key]
		return v, ok
	}
}

func TestLoadResolvesValuesDefaultsAndServicePorts(t *testing.T) {
	cfg, err := Load[sampleConfig](Options{
		Service: "commodore",
		Lookup: lookupFrom(map[string]string{
			"SAMPLE_TOKEN":   "secret-token",
			"SAMPLE_NAME":    "  commodore  ",
			"SAMPLE_ENABLED": "true",
			"SAMPLE_TIMEOUT": "750ms",
			"SAMPLE_ORIGINS": " https://a.example , ,https://b.example",
		}),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Token != "secret-token" || cfg.Name != "commodore" {
		t.Fatalf("string values = %q/%q", cfg.Token, cfg.Name)
	}
	if cfg.Mode != "debug" || cfg.Limit != 10 || !cfg.Enabled {
		t.Fatalf("defaults/bools = mode %q limit %d enabled %v", cfg.Mode, cfg.Limit, cfg.Enabled)
	}
	if cfg.Timeout != 750*time.Millisecond {
		t.Fatalf("timeout = %v", cfg.Timeout)
	}
	if want := []string{"https://a.example", "https://b.example"}; !reflect.DeepEqual(cfg.Origins, want) {
		t.Fatalf("origins = %#v, want %#v", cfg.Origins, want)
	}
	if cfg.Port != "18001" || cfg.GRPC != "19001" {
		t.Fatalf("servicedefs ports = %s/%s, want 18001/19001", cfg.Port, cfg.GRPC)
	}
}

func TestLoadReportsEveryMissingAndInvalidKeyWithoutValues(t *testing.T) {
	_, err := Load[sampleConfig](Options{
		Service: "commodore",
		Lookup: lookupFrom(map[string]string{
			"SAMPLE_NAME":    "   ",
			"SAMPLE_ENABLED": "definitely",
			"SAMPLE_LIMIT":   "ten-thousand",
		}),
	})
	var loadErr *LoadError
	if !errors.As(err, &loadErr) {
		t.Fatalf("expected LoadError, got %v", err)
	}
	if want := []string{"SAMPLE_TOKEN", "SAMPLE_NAME"}; !reflect.DeepEqual(loadErr.Missing, want) {
		t.Fatalf("missing = %v, want %v", loadErr.Missing, want)
	}
	if len(loadErr.Invalid) != 2 {
		t.Fatalf("invalid = %v, want SAMPLE_ENABLED and SAMPLE_LIMIT", loadErr.Invalid)
	}
	msg := err.Error()
	for _, leaked := range []string{"definitely", "ten-thousand"} {
		if strings.Contains(msg, leaked) {
			t.Fatalf("error %q leaks rejected value %q", msg, leaked)
		}
	}
}

func TestLoadRejectsMalformedAnnotationsBeforeReadingValues(t *testing.T) {
	cases := map[string]any{
		"missing desc": &struct {
			A string `env:"A_KEY" introduced:"v0.3.0"`
		}{},
		"short desc": &struct {
			A string `env:"A_KEY" desc:"short" introduced:"v0.3.0"`
		}{},
		"bad introduced": &struct {
			A string `env:"A_KEY" desc:"a valid description" introduced:"0.3.0"`
		}{},
		"prerelease introduced": &struct {
			A string `env:"A_KEY" desc:"a valid description" introduced:"v0.3.0-rc1"`
		}{},
		"replacement without deprecated": &struct {
			A string `env:"A_KEY" replacement:"B_KEY" desc:"a valid description" introduced:"v0.3.0"`
		}{},
		"required and default": &struct {
			A string `env:"A_KEY" required:"true" default:"x" desc:"a valid description" introduced:"v0.3.0"`
		}{},
		"unknown key": &struct {
			A string `env:"A_KEY" dfault:"x" desc:"a valid description" introduced:"v0.3.0"`
		}{},
		"lowercase env": &struct {
			A string `env:"a_key" desc:"a valid description" introduced:"v0.3.0"`
		}{},
		"required not true": &struct {
			A string `env:"A_KEY" required:"yes" desc:"a valid description" introduced:"v0.3.0"`
		}{},
		"unsupported type": &struct {
			A float64 `env:"A_KEY" desc:"a valid description" introduced:"v0.3.0"`
		}{},
		"untagged scalar": &struct {
			A string
		}{},
		"invalid default": &struct {
			A int `env:"A_KEY" default:"many" desc:"a valid description" introduced:"v0.3.0"`
		}{},
		"duplicate key": &struct {
			SampleBlock
			B string `env:"SAMPLE_TOKEN" desc:"a valid description" introduced:"v0.3.0"`
		}{},
		"service port without service": &struct {
			A string `env:"A_KEY" default:"@servicedefs.http_port" desc:"a valid description" introduced:"v0.3.0"`
		}{},
	}
	for name, target := range cases {
		t.Run(name, func(t *testing.T) {
			lookupCalled := false
			err := decode(target, Options{Lookup: func(string) (string, bool) {
				lookupCalled = true
				return "", false
			}})
			if err == nil {
				t.Fatal("expected an annotation error")
			}
			var loadErr *LoadError
			if errors.As(err, &loadErr) {
				t.Fatalf("annotation problem reported as a value error: %v", err)
			}
			if lookupCalled {
				t.Fatal("values were read before annotations were validated")
			}
		})
	}
}

type validatedConfig struct {
	Low  int `env:"RANGE_LOW" default:"1" desc:"Lower bound of the range." introduced:"v0.3.0"`
	High int `env:"RANGE_HIGH" default:"5" desc:"Upper bound of the range." introduced:"v0.3.0"`
}

func (c *validatedConfig) Validate() error {
	if c.Low > c.High {
		return errors.New("RANGE_LOW must not exceed RANGE_HIGH")
	}
	return nil
}

func TestLoadRunsValidateAfterDecoding(t *testing.T) {
	if _, err := Load[validatedConfig](Options{Lookup: lookupFrom(map[string]string{"RANGE_LOW": "9"})}); err == nil || !strings.Contains(err.Error(), "RANGE_LOW must not exceed") {
		t.Fatalf("expected Validate error, got %v", err)
	}
	if _, err := Load[validatedConfig](Options{Lookup: lookupFrom(nil)}); err != nil {
		t.Fatalf("valid defaults rejected: %v", err)
	}
}

func TestLoadWarnsWhenDeprecatedKeyIsSet(t *testing.T) {
	logger, hook := logtest.NewNullLogger()
	_, err := Load[sampleConfig](Options{
		Service: "commodore",
		Logger:  logger,
		Lookup:  lookupFrom(map[string]string{"SAMPLE_TOKEN": "t", "SAMPLE_NAME": "n", "SAMPLE_OLD": "legacy"}),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	entry := hook.LastEntry()
	if entry == nil || entry.Data["key"] != "SAMPLE_OLD" || entry.Data["replacement"] != "SAMPLE_NAME" {
		t.Fatalf("expected deprecation warning for SAMPLE_OLD, got %+v", entry)
	}
}

func TestDescribeRedactsSecretsAndReportsSource(t *testing.T) {
	opts := Options{Service: "commodore", Lookup: lookupFrom(map[string]string{"SAMPLE_TOKEN": "hunter2", "SAMPLE_NAME": "n"})}
	cfg, err := Load[sampleConfig](opts)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	fields, err := Describe(cfg, opts)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	byKey := map[string]FieldValue{}
	for _, f := range fields {
		byKey[f.Key] = f
	}
	if got := byKey["SAMPLE_TOKEN"]; got.Value != RedactedValue || !got.Secret || got.Source != "env" {
		t.Fatalf("secret field = %+v", got)
	}
	if got := byKey["SAMPLE_MODE"]; got.Value != "debug" || got.Source != "default" {
		t.Fatalf("defaulted field = %+v", got)
	}
	if got := byKey["SAMPLE_ORIGINS"]; got.Value != "" || got.Source != "unset" {
		t.Fatalf("unset field = %+v", got)
	}
	for _, f := range fields {
		if strings.Contains(f.Value, "hunter2") {
			t.Fatalf("Describe leaked secret in %+v", f)
		}
	}
}

func TestDescribeKeepsLoadedSourceAfterEnvironmentChanges(t *testing.T) {
	env := map[string]string{"SAMPLE_TOKEN": "secret", "SAMPLE_NAME": "loaded"}
	opts := Options{Service: "commodore", Lookup: lookupFrom(env)}
	cfg, err := Load[sampleConfig](opts)
	if err != nil {
		t.Fatal(err)
	}
	delete(env, "SAMPLE_NAME")
	env["SAMPLE_MODE"] = "new-value"
	fields, err := Describe(cfg, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range fields {
		if field.Key == "SAMPLE_NAME" && (field.Source != "env" || field.Value != "loaded") {
			t.Fatalf("lost original source: %+v", field)
		}
		if field.Key == "SAMPLE_MODE" && (field.Source != "default" || field.Value != "debug") {
			t.Fatalf("live env relabelled startup value: %+v", field)
		}
	}
}

func TestLiveReloadKeepsLastGoodConfig(t *testing.T) {
	values := map[string]string{"SAMPLE_TOKEN": "t", "SAMPLE_NAME": "first"}
	opts := Options{Service: "commodore", Lookup: lookupFrom(values)}
	cfg, err := Load[sampleConfig](opts)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	live := NewLive(cfg, opts)

	values["SAMPLE_NAME"] = "second"
	if err := live.Reload(); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if live.Get().Name != "second" {
		t.Fatalf("reload did not apply, name = %q", live.Get().Name)
	}

	delete(values, "SAMPLE_TOKEN")
	if err := live.Reload(); err == nil {
		t.Fatal("expected reload with a missing required key to fail")
	}
	if live.Get().Name != "second" || live.Get().Token != "t" {
		t.Fatalf("failed reload replaced the working config: %+v", live.Get())
	}
}

func TestSharedBlocksCarryValidAnnotations(t *testing.T) {
	type allBlocks struct {
		HTTPListen
		GRPCListen
		HTTPRuntime
		ServiceAuth
		GRPCTLS
		Postgres
		ClickHouse
		Registration
		BuildEnvironment
	}
	if err := checkAnnotations(reflect.TypeFor[allBlocks](), "periscope-query"); err != nil {
		t.Fatalf("shared blocks have invalid annotations: %v", err)
	}
}

func TestParseFieldTagIgnoresFieldsWithoutEnv(t *testing.T) {
	if _, ok, err := ParseFieldTag(`json:"name"`); ok || err != nil {
		t.Fatalf("non-config tag: ok=%v err=%v", ok, err)
	}
	if _, _, err := ParseFieldTag(`desc:"orphan description"`); err == nil {
		t.Fatal("config annotation without env must be rejected")
	}
	if _, _, err := ParseFieldTag(`env:"A" env:"B"`); err == nil {
		t.Fatal("duplicate tag key must be rejected")
	}
}
