package coordinator

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"frameworks/api_network_observer/internal/store"
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
)

func ObserverDefinitions(raw string) (map[string]obs.Observer, error) {
	var entries []obs.Observer
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, err
	}
	definitions := make(map[string]obs.Observer, len(entries))
	for _, entry := range entries {
		if entry.ID == "" || len(entry.ID) > 128 || strings.ContainsAny(entry.ID, "\r\n") || strings.HasPrefix(entry.ID, "gateway:") || len(entry.Region) > 128 {
			return nil, fmt.Errorf("invalid configured observer identity")
		}
		if _, exists := definitions[entry.ID]; exists {
			return nil, fmt.Errorf("duplicate configured observer %q", entry.ID)
		}
		if entry.Kind != "" && entry.Kind != "prober" {
			return nil, fmt.Errorf("configured observer %q must be a prober", entry.ID)
		}
		if entry.Location.Known && !entry.Location.Valid() {
			return nil, fmt.Errorf("invalid configured location for observer %q", entry.ID)
		}
		entry.Kind = "prober"
		if entry.Location.Valid() {
			entry.Location.Source = "configured"
		}
		definitions[entry.ID] = entry
	}
	return definitions, nil
}

// Reconciliation changes configuration without resetting delivery evidence.
func ReconcileObserverDefinitions(s *store.State, definitions map[string]obs.Observer, now time.Time) {
	for id, old := range s.Observers {
		definition, exists := definitions[id]
		if old.Kind == "prober" && old.Location.Source == "configured" && (!exists || !definition.Location.Valid()) {
			old.Location = obs.Location{Source: "unknown"}
			old.LocationUpdatedAt = now
			s.Observers[id] = old
		}
	}
	for id, definition := range definitions {
		current := s.Observers[id]
		current.ID, current.Kind = id, "prober"
		if definition.Region != "" {
			current.Region = definition.Region
		}
		if definition.Location.Valid() && current.Location != definition.Location {
			current.Location = definition.Location
			current.LocationUpdatedAt = now
		}
		s.Observers[id] = current
	}
}
