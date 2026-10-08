package store

import (
	obs "github.com/Livepeer-FrameWorks/monorepo/pkg/networkobservation"
	"math"
	"testing"
)

func TestInvalidDocumentCannotSilentlyBecomeEmptyJSON(t *testing.T) {
	state := NewState()
	state.Endpoints["endpoint"] = obs.Endpoint{Location: obs.Location{Latitude: math.NaN()}}
	if _, err := state.documents(); err == nil {
		t.Fatal("invalid floating point data must reject the transaction")
	}
}
