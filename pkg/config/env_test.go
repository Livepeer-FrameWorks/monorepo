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

func TestGetEnvInt(t *testing.T) {
	t.Setenv("NUM", "")
	if got := GetEnvInt("NUM", 42); got != 42 {
		t.Fatalf("expected 42, got %d", got)
	}
	t.Setenv("NUM", "100")
	if got := GetEnvInt("NUM", 42); got != 100 {
		t.Fatalf("expected 100, got %d", got)
	}
	t.Setenv("NUM", "notint")
	if got := GetEnvInt("NUM", 7); got != 7 {
		t.Fatalf("expected 7 on parse error, got %d", got)
	}
}

func TestGetEnvBool(t *testing.T) {
	t.Setenv("FLAG", "")
	if got := GetEnvBool("FLAG", true); got != true {
		t.Fatalf("expected true default, got %v", got)
	}
	t.Setenv("FLAG", "false")
	if got := GetEnvBool("FLAG", true); got != false {
		t.Fatalf("expected false, got %v", got)
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

func TestIsDevelopmentUsesBuildEnvOnly(t *testing.T) {
	t.Setenv("BUILD_ENV", "")
	if !IsDevelopment() {
		t.Fatalf("expected empty BUILD_ENV to default to development")
	}

	t.Setenv("BUILD_ENV", "development")
	if !IsDevelopment() {
		t.Fatalf("expected BUILD_ENV=development to report development")
	}

	t.Setenv("BUILD_ENV", "production")
	if IsDevelopment() {
		t.Fatalf("expected BUILD_ENV=production to report non-development")
	}
}
