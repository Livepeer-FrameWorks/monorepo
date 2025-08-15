package version

import "testing"

func TestGetInfo(t *testing.T) {
	info := GetInfo()
	if info.Version == "" || info.GitCommit == "" || info.BuildDate == "" {
		t.Fatalf("expected non-empty version info")
	}
}

func TestGetShortCommit(t *testing.T) {
	GitCommit = "abcdef123456"
	if GetShortCommit() != "abcdef1" {
		t.Fatalf("expected short commit")
	}
}
