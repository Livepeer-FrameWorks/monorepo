package tools

import (
	"frameworks/api_gateway/graph/model"
	"github.com/google/jsonschema-go/jsonschema"
)

// Descriptions supplement shared generated GraphQL inputs without maintaining
// a second set of policy DTOs. Enum values come from the same generated model.
var placementPropertyDescriptions = map[string]string{
	"scope":                  "Policy scope: TENANT defaults or a STREAM overlay within the authenticated tenant.",
	"kind":                   "Scope, update or selector kind; use the enumerated values for this field.",
	"streamId":               "Stream identifier owned by the authenticated tenant; required for STREAM scope.",
	"clusterId":              "Exact media cluster whose owning tenant must authorize the consent operation.",
	"filter":                 "Optional authorized selector search filters.",
	"query":                  "Search text for tenant-visible placement options.",
	"after":                  "Opaque cursor returned by the previous options page; retain the same scope and filters.",
	"first":                  "Page size from 1 through 100.",
	"verb":                   "INGEST places the publisher; SERVE places viewers using the viewer location.",
	"protocol":               "Media protocol to evaluate, such as rtmp, srt, whip, hls or webrtc; must be supported for the selected verb.",
	"coordinates":            "Hypothetical client location for preview; does not change account location or reserve capacity.",
	"latitude":               "Client latitude in decimal degrees, from -90 through 90.",
	"longitude":              "Client longitude in decimal degrees, from -180 through 180.",
	"draftUpdate":            "Optional single-verb draft. Omit to evaluate saved policy; CLEAR previews inheritance. Requires both expected revisions.",
	"expectedRevision":       "Exact saved policy or consent revision as a decimal string, not a JSON number.",
	"expectedParentRevision": "Exact tenant-parent revision as a decimal string for a stream overlay; use 0 for tenant scope.",
	"updates":                "Atomic unique per-verb updates. Unmentioned verbs stay unchanged. SET includes complete rules; CLEAR omits rules.",
	"rules":                  "Complete rules for SET; must be absent for CLEAR.",
	"schemaVersion":          "Policy rule schema version; currently 1.",
	"constraints":            "Hard eligibility restrictions; preferences and spillover cannot override them.",
	"allow":                  "Omit for no additional allow restriction. An explicit empty any list allows no destinations.",
	"any":                    "OR of allowed selectors. An empty list denies all destinations.",
	"deny":                   "Selectors to exclude, regardless of preference ranking.",
	"preferences":            "Omit to inherit preference order; an explicit empty groups list denies all destinations.",
	"groups":                 "Ordered preference groups, evaluated in order with explicit spillover conditions.",
	"id":                     "Stable unique preference-group identifier, retained across edits and warning references.",
	"match":                  "Selector fields combine with AND; values within each field combine with OR. Empty matches all entitled capacity.",
	"clusterIds":             "Exact authorized cluster identifiers to match.",
	"ownerIds":               "Authorized cluster-owner tenant identifiers to match.",
	"regions":                "Authorized region identifiers to match.",
	"classes":                "Cluster classes to match; restrictions remain independent of charging class.",
	"charging":               "RATED or PERMANENTLY_FREE capacity. Missing quotes do not imply free capacity.",
	"order":                  "Rank eligible capacity by client distance or comparable quoted price within this group.",
	"spillover":              "Conditions that permit leaving this preference group; NEVER forbids fallback.",
	"maxDistanceKm":          "Hard maximum client-to-destination distance in kilometers; zero is unbounded.",
	"geoHoleDistanceKm":      "Soft distance threshold in kilometers; must be positive for geographic spillover.",
	"minImprovementKm":       "Minimum distance improvement in kilometers required for geographic spillover.",
	"priceCurrency":          "Required comparable quote currency when ordering by PRICE.",
	"priceUnit":              "Required comparable quote unit when ordering by PRICE.",
	"reviewToken":            "Opaque, expiring token returned by review for exactly these updates and revisions.",
	"idempotencyKey":         "Stable key for this exact command. Recover the same key after an uncertain apply; never generate a new key automatically.",
	"acknowledgedWarningIds": "Exact warning identifiers explicitly acknowledged after inspecting the review.",
	"allowIngest":            "Whether the owning cluster consents to receiving publishers.",
	"allowServe":             "Whether the owning cluster consents to serving viewers.",
	"allowExternalSource":    "Whether the owning cluster consents to pulling media sources from outside the cluster.",
}

func placementEnumValues[T ~string](values []T) []any {
	result := make([]any, len(values))
	for i, value := range values {
		result[i] = string(value)
	}
	return result
}

func describePlacementSchema(schema *jsonschema.Schema) {
	if schema == nil {
		return
	}
	for name, property := range schema.Properties {
		if description := placementPropertyDescriptions[name]; description != "" {
			property.Description = description
		}
		switch name {
		case "kind":
			switch {
			case schema.Properties["streamId"] != nil:
				property.Enum = placementEnumValues(model.AllMediaPlacementScopeKind)
			case schema.Properties["rules"] != nil:
				property.Enum = placementEnumValues(model.AllMediaPlacementUpdateKind)
			case schema.Properties["query"] != nil:
				property.Enum = append(placementEnumValues(model.AllMediaPlacementOptionKind), nil)
			}
		case "verb":
			property.Enum = placementEnumValues(model.AllMediaPlacementVerb)
		case "order":
			property.Enum = placementEnumValues(model.AllMediaPlacementOrder)
		case "spillover":
			property.Enum = placementEnumValues(model.AllMediaPlacementSpillover)
		case "classes":
			if property.Items != nil {
				property.Items.Enum = placementEnumValues(model.AllMediaPlacementClass)
			}
		case "charging":
			if property.Items != nil {
				property.Items.Enum = placementEnumValues(model.AllMediaPlacementCharging)
			}
		case "latitude":
			property.Minimum, property.Maximum = number(-90), number(90)
		case "longitude":
			property.Minimum, property.Maximum = number(-180), number(180)
		case "first":
			property.Minimum, property.Maximum = number(1), number(100)
		case "maxDistanceKm", "geoHoleDistanceKm", "minImprovementKm":
			property.Minimum = number(0)
		}
		describePlacementSchema(property)
	}
	for _, definition := range schema.Defs {
		describePlacementSchema(definition)
	}
	for _, definition := range schema.Definitions {
		describePlacementSchema(definition)
	}
	describePlacementSchema(schema.Items)
	for _, children := range [][]*jsonschema.Schema{schema.AllOf, schema.AnyOf, schema.OneOf} {
		for _, child := range children {
			describePlacementSchema(child)
		}
	}
}
