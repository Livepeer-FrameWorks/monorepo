// Package geoip provides MMDB-based IP geolocation for FrameWorks
//
// Supports multiple MMDB providers:
// - MaxMind GeoLite2 (requires license key from user)
// - DB-IP Lite (CC BY 4.0, redistributable)
// - IP2Location LITE (CC BY-SA 4.0, redistributable)
//
// Usage:
//
//	reader, err := geoip.NewReader("/path/to/database.mmdb")
//	if err != nil {
//	    // GeoIP disabled - handle gracefully
//	    return
//	}
//	defer reader.Close()
//
//	geoData := reader.Lookup("192.168.1.1")
//	if geoData != nil {
//	    fmt.Printf("Country: %s, City: %s\n", geoData.CountryCode, geoData.City)
//	}
package geoip

import (
	"net"
	"path/filepath"
	"strings"

	"github.com/oschwald/geoip2-golang"
)

// GeoData contains geolocation information for an IP address
type GeoData struct {
	CountryCode string  `json:"country_code,omitempty"`
	CountryName string  `json:"country_name,omitempty"`
	City        string  `json:"city,omitempty"`
	Latitude    float64 `json:"latitude,omitempty"`
	Longitude   float64 `json:"longitude,omitempty"`
	Timezone    string  `json:"timezone,omitempty"`
}

// Reader provides IP geolocation lookups using MMDB databases
type Reader struct {
	db                  *geoip2.Reader
	provider            string
	requiresAttribution bool
	attributionText     string
	dbPath              string
}

// NewReader creates a new GeoIP reader from an MMDB file
//
// Returns nil, nil if the file doesn't exist (graceful degradation)
// Returns nil, error if the file exists but can't be opened
func NewReader(mmdbPath string) (*Reader, error) {
	if mmdbPath == "" {
		return nil, nil // No database path provided
	}

	db, err := geoip2.Open(mmdbPath)
	if err != nil {
		// Check if file doesn't exist vs other errors
		if strings.Contains(err.Error(), "no such file") || strings.Contains(err.Error(), "cannot find") {
			return nil, nil // File doesn't exist - graceful degradation
		}
		return nil, err // Real error opening file
	}

	// Detect provider and attribution requirements from filename
	provider, requiresAttribution, attributionText := detectProvider(mmdbPath)

	return &Reader{
		db:                  db,
		provider:            provider,
		requiresAttribution: requiresAttribution,
		attributionText:     attributionText,
		dbPath:              mmdbPath,
	}, nil
}

// detectProvider attempts to identify the MMDB provider from filename
func detectProvider(mmdbPath string) (provider string, requiresAttribution bool, attributionText string) {
	filename := strings.ToLower(filepath.Base(mmdbPath))

	switch {
	case strings.Contains(filename, "geolite2") || strings.Contains(filename, "maxmind"):
		return "maxmind", true, "This product includes GeoLite2 data created by MaxMind, available from https://www.maxmind.com."

	case strings.Contains(filename, "dbip") || strings.Contains(filename, "db-ip"):
		return "dbip", true, "IP Geolocation by DB-IP (https://db-ip.com)"

	case strings.Contains(filename, "ip2location"):
		return "ip2location", true, "This site or product includes IP2Location LITE data available from https://lite.ip2location.com."

	default:
		return "unknown", false, ""
	}
}

// Lookup performs a geolocation lookup for the given IP address
//
// Returns nil if:
// - No database is loaded
// - IP is invalid
// - IP is not found in database
// - IP is a private/local address
func (r *Reader) Lookup(ipStr string) *GeoData {
	if r == nil || r.db == nil {
		return nil
	}

	// Handle "ip:port" format by extracting just the IP
	host, _, err := net.SplitHostPort(ipStr)
	if err != nil {
		host = ipStr // Assume it's already just an IP
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}

	// Skip private/local IPs (they won't be in geo databases anyway)
	if isPrivateIP(ip) {
		return nil
	}

	record, err := r.db.City(ip)
	if err != nil {
		return nil
	}

	geoData := &GeoData{}

	// Extract country information
	if record.Country.IsoCode != "" {
		geoData.CountryCode = record.Country.IsoCode
	}
	if record.Country.Names["en"] != "" {
		geoData.CountryName = record.Country.Names["en"]
	}

	// Extract city information
	if record.City.Names["en"] != "" {
		geoData.City = record.City.Names["en"]
	}

	// Extract coordinates (only if non-zero)
	if record.Location.Latitude != 0 {
		geoData.Latitude = record.Location.Latitude
	}
	if record.Location.Longitude != 0 {
		geoData.Longitude = record.Location.Longitude
	}

	// Extract timezone
	if record.Location.TimeZone != "" {
		geoData.Timezone = record.Location.TimeZone
	}

	// Return nil if we didn't get any useful data
	if geoData.CountryCode == "" && geoData.City == "" {
		return nil
	}

	return geoData
}

// isPrivateIP checks if an IP address is private/local
func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}

	// Check for private IPv4 ranges
	if ip.To4() != nil {
		return ip.IsPrivate()
	}

	// Check for private IPv6 ranges
	return ip.IsPrivate()
}

// GetProvider returns the detected provider name
func (r *Reader) GetProvider() string {
	if r == nil {
		return "none"
	}
	return r.provider
}

// RequiresAttribution returns whether this provider requires attribution
func (r *Reader) RequiresAttribution() bool {
	if r == nil {
		return false
	}
	return r.requiresAttribution
}

// GetAttributionText returns the attribution text for this provider
func (r *Reader) GetAttributionText() string {
	if r == nil {
		return ""
	}
	return r.attributionText
}

// GetDatabasePath returns the path to the loaded database file
func (r *Reader) GetDatabasePath() string {
	if r == nil {
		return ""
	}
	return r.dbPath
}

// Close closes the underlying database
func (r *Reader) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

// IsLoaded returns true if a database is successfully loaded
func (r *Reader) IsLoaded() bool {
	return r != nil && r.db != nil
}

// EnrichEvent adds geo fields to an event map if geo data is available
//
// This is a convenience function for enriching Kafka events.
// Only adds fields if geo data exists - never adds nil/empty fields.
func (r *Reader) EnrichEvent(event map[string]interface{}, ipField string) {
	if r == nil || event == nil {
		return
	}

	ipStr, ok := event[ipField].(string)
	if !ok || ipStr == "" {
		return
	}

	geoData := r.Lookup(ipStr)
	if geoData == nil {
		return
	}

	// Only add fields that have actual values
	if geoData.CountryCode != "" {
		event["country_code"] = geoData.CountryCode
	}
	if geoData.CountryName != "" {
		event["country_name"] = geoData.CountryName
	}
	if geoData.City != "" {
		event["city"] = geoData.City
	}
	if geoData.Latitude != 0 {
		event["latitude"] = geoData.Latitude
	}
	if geoData.Longitude != 0 {
		event["longitude"] = geoData.Longitude
	}
	if geoData.Timezone != "" {
		event["timezone"] = geoData.Timezone
	}
}
