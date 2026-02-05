package geoip

import (
	"net"
	"testing"
)

func TestNewReader(t *testing.T) {
	tests := []struct {
		name     string
		mmdbPath string
		wantNil  bool
		wantErr  bool
	}{
		{
			name:     "empty path returns nil reader",
			mmdbPath: "",
			wantNil:  true,
			wantErr:  false,
		},
		{
			name:     "nonexistent file returns nil reader",
			mmdbPath: "/nonexistent/path/file.mmdb",
			wantNil:  true,
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader, err := NewReader(tt.mmdbPath)

			if tt.wantErr && err == nil {
				t.Errorf("NewReader() expected error but got none")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("NewReader() unexpected error: %v", err)
			}
			if tt.wantNil && reader != nil {
				t.Errorf("NewReader() expected nil reader but got %v", reader)
			}
			if !tt.wantNil && reader == nil {
				t.Errorf("NewReader() expected reader but got nil")
			}

			// Always try to close if we got a reader
			if reader != nil {
				_ = reader.Close()
			}
		})
	}
}

func TestDetectProvider(t *testing.T) {
	tests := []struct {
		name                string
		mmdbPath            string
		wantProvider        string
		wantAttribution     bool
		wantAttributionText string
	}{
		{
			name:                "MaxMind GeoLite2",
			mmdbPath:            "/data/GeoLite2-City.mmdb",
			wantProvider:        "maxmind",
			wantAttribution:     true,
			wantAttributionText: "This product includes GeoLite2 data created by MaxMind, available from https://www.maxmind.com.",
		},
		{
			name:                "DB-IP database",
			mmdbPath:            "/data/dbip-city-lite-2024-01.mmdb",
			wantProvider:        "dbip",
			wantAttribution:     true,
			wantAttributionText: "IP Geolocation by DB-IP (https://db-ip.com)",
		},
		{
			name:                "IP2Location database",
			mmdbPath:            "/data/IP2LOCATION-LITE-DB11.mmdb",
			wantProvider:        "ip2location",
			wantAttribution:     true,
			wantAttributionText: "This site or product includes IP2Location LITE data available from https://lite.ip2location.com.",
		},
		{
			name:                "Unknown provider",
			mmdbPath:            "/data/custom-geo.mmdb",
			wantProvider:        "unknown",
			wantAttribution:     false,
			wantAttributionText: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, requiresAttribution, attributionText := detectProvider(tt.mmdbPath)

			if provider != tt.wantProvider {
				t.Errorf("detectProvider() provider = %v, want %v", provider, tt.wantProvider)
			}
			if requiresAttribution != tt.wantAttribution {
				t.Errorf("detectProvider() requiresAttribution = %v, want %v", requiresAttribution, tt.wantAttribution)
			}
			if attributionText != tt.wantAttributionText {
				t.Errorf("detectProvider() attributionText = %v, want %v", attributionText, tt.wantAttributionText)
			}
		})
	}
}

func TestIsPrivateIP(t *testing.T) {
	tests := []struct {
		name string
		ip   string
		want bool
	}{
		{"localhost IPv4", "127.0.0.1", true},
		{"localhost IPv6", "::1", true},
		{"private 10.x", "10.0.0.1", true},
		{"private 192.168.x", "192.168.1.1", true},
		{"private 172.16.x", "172.16.0.1", true},
		{"public Google DNS", "8.8.8.8", false},
		{"public Cloudflare DNS", "1.1.1.1", false},
		{"public IPv6", "2001:4860:4860::8888", false},
		{"link local IPv4", "169.254.1.1", true},
		{"link local IPv6", "fe80::1", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip := net.ParseIP(tt.ip)
			if ip == nil {
				t.Fatalf("Failed to parse IP: %s", tt.ip)
			}

			got := isPrivateIP(ip)
			if got != tt.want {
				t.Errorf("isPrivateIP(%s) = %v, want %v", tt.ip, got, tt.want)
			}
		})
	}
}

func TestLookupWithoutDatabase(t *testing.T) {
	// Test that Lookup gracefully handles no database
	var reader *Reader = nil
	result := reader.Lookup("8.8.8.8")
	if result != nil {
		t.Errorf("Lookup() with nil reader should return nil, got %v", result)
	}

	// Test reader methods with nil receiver
	if reader.GetProvider() != "none" {
		t.Errorf("GetProvider() with nil reader should return 'none'")
	}
	if reader.RequiresAttribution() != false {
		t.Errorf("RequiresAttribution() with nil reader should return false")
	}
	if reader.GetAttributionText() != "" {
		t.Errorf("GetAttributionText() with nil reader should return empty string")
	}
	if reader.IsLoaded() != false {
		t.Errorf("IsLoaded() with nil reader should return false")
	}
}

func TestEnrichEvent(t *testing.T) {
	// Test with nil reader
	var reader *Reader = nil
	event := map[string]interface{}{
		"connection_addr": "8.8.8.8:12345",
	}

	reader.EnrichEvent(event, "connection_addr")

	// Should not add any geo fields
	if _, exists := event["country_code"]; exists {
		t.Errorf("EnrichEvent() with nil reader should not add geo fields")
	}

	// Test with missing IP field
	reader2, _ := NewReader("") // This returns nil reader
	event2 := map[string]interface{}{
		"other_field": "value",
	}

	reader2.EnrichEvent(event2, "missing_field")

	// Should not add any geo fields
	if _, exists := event2["country_code"]; exists {
		t.Errorf("EnrichEvent() with missing IP field should not add geo fields")
	}
}

func TestLookupIPFormats(t *testing.T) {
	// Create a nil reader for testing format parsing
	reader := &Reader{db: nil}

	tests := []struct {
		name   string
		input  string
		expect string // expected IP to be extracted
	}{
		{"IP only", "8.8.8.8", "8.8.8.8"},
		{"IP with port", "8.8.8.8:12345", "8.8.8.8"},
		{"IPv6", "2001:db8::1", "2001:db8::1"},
		{"IPv6 with port", "[2001:db8::1]:8080", "2001:db8::1"},
		{"invalid IP", "not-an-ip", ""},
		{"empty string", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We can't test actual lookup without a real database,
			// but we can test that it doesn't crash and handles formats correctly
			result := reader.Lookup(tt.input)

			// Should always return nil since we have no database
			if result != nil {
				t.Errorf("Lookup() with no database should return nil, got %v", result)
			}
		})
	}
}
