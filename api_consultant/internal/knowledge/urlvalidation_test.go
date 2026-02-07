package knowledge

import (
	"context"
	"net"
	"testing"
)

func TestValidateCrawlURL(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"valid https", "https://example.com/sitemap.xml", false},
		{"valid http", "http://example.com/page", false},
		{"ftp scheme blocked", "ftp://example.com/file", true},
		{"file scheme blocked", "file:///etc/passwd", true},
		{"javascript scheme blocked", "javascript:alert(1)", true},
		{"empty string", "", true},
		{"no scheme", "example.com", true},
		{"localhost", "http://localhost/admin", true},
		{"127.0.0.1", "http://127.0.0.1/", true},
		{"[::1]", "http://[::1]/", true},
		{"metadata endpoint", "http://169.254.169.254/latest/meta-data/", true},
		{"private 10.x", "http://10.0.0.1/internal", true},
		{"private 172.16.x", "http://172.16.0.1/internal", true},
		{"private 192.168.x", "http://192.168.1.1/internal", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateCrawlURLWithContext(context.Background(), tt.url)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateCrawlURLWithContext(%q) error = %v, wantErr %v", tt.url, err, tt.wantErr)
			}
		})
	}
}

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		ip      string
		private bool
	}{
		{"127.0.0.1", true},
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"172.32.0.1", false},
		{"192.168.0.1", true},
		{"169.254.169.254", true},
		{"100.64.0.1", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"::1", true},
		{"fe80::1", true},
		{"fd00::1", true},
	}

	for _, tt := range tests {
		t.Run(tt.ip, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("invalid test IP: %s", tt.ip)
			}
			got := isPrivateIP(ip)
			if got != tt.private {
				t.Errorf("isPrivateIP(%s) = %v, want %v", tt.ip, got, tt.private)
			}
		})
	}
}
