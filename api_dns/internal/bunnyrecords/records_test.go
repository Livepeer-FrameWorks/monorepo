package bunnyrecords

import (
	"testing"

	"frameworks/api_dns/internal/provider/bunny"
)

func TestARecordGeoRoutingRequiresValidCoordinates(t *testing.T) {
	lat := 52.5083
	lon := 5.4750
	zero := 0.0

	tests := []struct {
		name string
		geo  GeoCoordinates
		want int
	}{
		{name: "no coordinates", want: bunny.SmartRoutingNone},
		{
			name: "zero coordinates rejected",
			geo:  GeoCoordinates{Latitude: &zero, Longitude: &zero},
			want: bunny.SmartRoutingNone,
		},
		{
			name: "valid coordinates enable geo routing",
			geo:  GeoCoordinates{Latitude: &lat, Longitude: &lon},
			want: bunny.SmartRoutingGeolocation,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := ARecord(ARecordInput{
				Name:      "edge.frameworks",
				Value:     "89.105.216.3",
				TTL:       60,
				FQDN:      "edge.frameworks.cdn.frameworks.network",
				Geography: tt.geo,
			})
			if record.SmartRoutingType != tt.want {
				t.Fatalf("SmartRoutingType = %d, want %d", record.SmartRoutingType, tt.want)
			}
			if tt.want == bunny.SmartRoutingNone && (record.GeolocationLatitude != nil || record.GeolocationLongitude != nil) {
				t.Fatalf("unexpected coordinates on non-geo record: %#v", record)
			}
			if tt.want == bunny.SmartRoutingGeolocation && (record.GeolocationLatitude == nil || record.GeolocationLongitude == nil) {
				t.Fatalf("expected coordinates on geo record: %#v", record)
			}
		})
	}
}
