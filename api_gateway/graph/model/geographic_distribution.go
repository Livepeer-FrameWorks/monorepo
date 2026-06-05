package model

import (
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
)

// GeographicDistribution is a manually-defined type for geographic analytics.
// Must be separate from models_gen.go to survive codegen deletion.
type GeographicDistribution struct {
	TimeRange        *commonpb.TimeRange          `json:"timeRange"`
	Stream           *string                      `json:"stream,omitempty"`
	TopCountries     []*periscopepb.CountryMetric `json:"topCountries"`
	TopCities        []*periscopepb.CityMetric    `json:"topCities"`
	UniqueCountries  int                          `json:"uniqueCountries"`
	UniqueCities     int                          `json:"uniqueCities"`
	TotalViewers     int                          `json:"totalViewers"`
	ViewersByCountry []*CountryTimeSeries         `json:"viewersByCountry"`
}
