package model

import (
	"frameworks/pkg/proto"
)

// GeographicDistribution is a manually-defined type for geographic analytics.
// Must be separate from models_gen.go to survive codegen deletion.
type GeographicDistribution struct {
	TimeRange        *proto.TimeRange       `json:"timeRange"`
	Stream           *string                `json:"stream,omitempty"`
	TopCountries     []*proto.CountryMetric `json:"topCountries"`
	TopCities        []*proto.CityMetric    `json:"topCities"`
	UniqueCountries  int                    `json:"uniqueCountries"`
	UniqueCities     int                    `json:"uniqueCities"`
	TotalViewers     int                    `json:"totalViewers"`
	ViewersByCountry []*CountryTimeSeries   `json:"viewersByCountry"`
}
