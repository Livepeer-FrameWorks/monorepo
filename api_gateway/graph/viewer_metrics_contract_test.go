package graph

import (
	"strings"
	"testing"

	"frameworks/api_gateway/graph/generated"
)

func TestViewerBandwidthUnitsAndDirection(t *testing.T) {
	schema := generated.NewExecutableSchema(generated.Config{Resolvers: &Resolver{}}).Schema()
	for field, direction := range map[string]string{
		"bandwidthInBps":  "sent to the viewer",
		"bandwidthOutBps": "received from the viewer",
	} {
		description := schema.Types["ViewerMetrics"].Fields.ForName(field).Description
		if !strings.Contains(description, "bytes per second") || !strings.Contains(description, direction) {
			t.Errorf("ViewerMetrics.%s description=%q; want bytes per second %s", field, description, direction)
		}
	}
}
