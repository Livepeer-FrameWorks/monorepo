package sops

import "testing"

func TestIsEncrypted(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
	}{
		{"dotenv with sops_version", "FOO=bar\nsops_version=3.7.0", true},
		{"dotenv with ENC marker", "FOO=ENC[AES256_GCM,abc123]", true},
		{"plain dotenv", "FOO=bar\nBAZ=qux", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsEncrypted([]byte(tt.data)); got != tt.want {
				t.Errorf("IsEncrypted() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsEncryptedYAML(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
	}{
		{
			"sops yaml with metadata",
			"hosts:\n  node-1:\n    ip: ENC[AES256_GCM,abc]\nsops:\n  version: 3.7.0",
			true,
		},
		{
			"sops yaml metadata at start",
			"sops:\n  version: 3.7.0\nhosts:\n  ip: ENC[AES256_GCM,abc]",
			true,
		},
		{
			"plain yaml",
			"hosts:\n  node-1:\n    ip: 10.0.0.1",
			false,
		},
		{
			"dotenv with sops_version (not yaml)",
			"FOO=bar\nsops_version=3.7.0",
			false,
		},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsEncryptedYAML([]byte(tt.data)); got != tt.want {
				t.Errorf("IsEncryptedYAML() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestFormatFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"secrets/production.env", "dotenv"},
		{"clusters/prod/hosts.enc.yaml", "yaml"},
		{"config.yml", "yaml"},
		{"data.json", "json"},
		{"no-extension", "dotenv"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := FormatFromPath(tt.path); got != tt.want {
				t.Errorf("FormatFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
