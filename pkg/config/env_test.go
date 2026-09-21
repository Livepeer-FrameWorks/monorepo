package config

import (
	"testing"

	"github.com/sirupsen/logrus"
)

func TestGetEnvWithDefault(t *testing.T) {
	t.Setenv("FOO", "")
	if got := GetEnv("FOO", "bar"); got != "bar" {
		t.Fatalf("expected bar, got %s", got)
	}
	t.Setenv("FOO", "baz")
	if got := GetEnv("FOO", "bar"); got != "baz" {
		t.Fatalf("expected baz, got %s", got)
	}
}

func TestGetLogLevel(t *testing.T) {
	t.Setenv("LOG_LEVEL", "debug")
	if GetLogLevel() != logrus.DebugLevel {
		t.Fatalf("expected debug level")
	}
	t.Setenv("LOG_LEVEL", "warn")
	if GetLogLevel() != logrus.WarnLevel {
		t.Fatalf("expected warn level")
	}
	t.Setenv("LOG_LEVEL", "error")
	if GetLogLevel() != logrus.ErrorLevel {
		t.Fatalf("expected error level")
	}
	t.Setenv("LOG_LEVEL", "")
	if GetLogLevel() != logrus.InfoLevel {
		t.Fatalf("expected info level by default")
	}
}

func TestLoadEnv_NoFile(t *testing.T) {
	// Should not panic or error; just log debug
	logger := logrus.New()
	LoadEnv(logger)
}

func TestIsProductionUsesBuildEnvOnly(t *testing.T) {
	t.Setenv("BUILD_ENV", "production")
	t.Setenv("NODE_ENV", "development")
	t.Setenv("GO_ENV", "development")
	if !IsProduction() {
		t.Fatalf("expected BUILD_ENV=production to report production")
	}

	t.Setenv("BUILD_ENV", "development")
	t.Setenv("NODE_ENV", "production")
	t.Setenv("GO_ENV", "production")
	if IsProduction() {
		t.Fatalf("expected BUILD_ENV=development to win over NODE_ENV/GO_ENV")
	}
}

func TestBuildEnvironmentIsDevelopment(t *testing.T) {
	for value, want := range map[string]bool{"": true, "dev": true, " Development ": true, "production": false, "staging": false} {
		if got := (BuildEnvironment{BuildEnv: value}).IsDevelopment(); got != want {
			t.Fatalf("BuildEnvironment{%q}.IsDevelopment() = %v, want %v", value, got, want)
		}
	}
}
