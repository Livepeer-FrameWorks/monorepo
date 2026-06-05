package bunnyrecords

import (
	"fmt"
	"math"
	"strings"

	"frameworks/api_dns/internal/provider/bunny"
)

// GeoCoordinates is the optional per-record location Bunny needs for geo DNS.
type GeoCoordinates struct {
	Latitude  *float64
	Longitude *float64
}

// ARecordInput describes the shared Bunny A-record shape Navigator publishes.
type ARecordInput struct {
	Name        string
	Value       string
	TTL         int
	Weight      int
	FQDN        string
	Comment     string
	Geography   GeoCoordinates
	MonitorType int
}

// ARecord builds the canonical Bunny A record. Geo routing is enabled only
// when both coordinates are present and valid; callers without validated
// coordinates publish a normal non-geo A record.
func ARecord(input ARecordInput) bunny.Record {
	weight := input.Weight
	if weight == 0 {
		weight = 100
	}
	monitorType := input.MonitorType
	if monitorType == 0 {
		monitorType = bunny.MonitorTypeNone
	}
	comment := input.Comment
	if comment == "" {
		comment = managedComment(input.FQDN)
	}

	record := bunny.Record{
		Type:             bunny.RecordTypeA,
		Name:             input.Name,
		Value:            input.Value,
		TTL:              input.TTL,
		Weight:           weight,
		MonitorType:      monitorType,
		SmartRoutingType: bunny.SmartRoutingNone,
		Comment:          comment,
	}
	if input.Geography.Latitude != nil &&
		input.Geography.Longitude != nil &&
		validLatLon(*input.Geography.Latitude, *input.Geography.Longitude) {
		record.SmartRoutingType = bunny.SmartRoutingGeolocation
		record.GeolocationLatitude = input.Geography.Latitude
		record.GeolocationLongitude = input.Geography.Longitude
	}
	return record
}

func validLatLon(lat, lon float64) bool {
	if math.IsNaN(lat) || math.IsNaN(lon) || math.IsInf(lat, 0) || math.IsInf(lon, 0) {
		return false
	}
	if lat < -90 || lat > 90 {
		return false
	}
	if lon < -180 || lon > 180 {
		return false
	}
	return lat != 0 || lon != 0
}

func managedComment(fqdn string) string {
	fqdn = strings.TrimSpace(fqdn)
	if fqdn == "" {
		return "Managed by Navigator"
	}
	return fmt.Sprintf("Managed by Navigator for %s", fqdn)
}
