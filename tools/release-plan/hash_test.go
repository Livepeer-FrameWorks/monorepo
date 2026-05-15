package main

import (
	"slices"
	"strings"
	"testing"
)

func TestCanonicalGoListEnvPinsTargetPlatform(t *testing.T) {
	env := canonicalGoListEnv([]string{
		"GOOS=darwin",
		"GOARCH=arm64",
		"CGO_ENABLED=0",
		"GOFLAGS=-tags=local",
		"PATH=/bin",
	}, true)

	for _, forbidden := range []string{"GOOS=darwin", "GOARCH=arm64", "GOFLAGS=-tags=local"} {
		if slices.Contains(env, forbidden) {
			t.Fatalf("env contains caller override %q: %v", forbidden, env)
		}
	}
	for _, want := range []string{"PATH=/bin", "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=1", "GOFLAGS="} {
		if !slices.Contains(env, want) {
			t.Fatalf("env missing %q: %v", want, env)
		}
	}
}

func TestCanonicalGoListEnvDisablesCGOForPureGo(t *testing.T) {
	env := canonicalGoListEnv(nil, false)
	got := strings.Join(env, "\n")
	if !strings.Contains(got, "CGO_ENABLED=0") {
		t.Fatalf("CGO_ENABLED not disabled for pure-Go component: %v", env)
	}
}
