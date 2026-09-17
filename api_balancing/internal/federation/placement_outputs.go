package federation

import (
	"encoding/json"
	"errors"
)

const maxPlacementOutputsJSON = 1 << 20

func encodePlacementOutputs(outputs map[string]any) (string, error) {
	if len(outputs) == 0 {
		return "", errors.New("selected destination has no Mist output advertisement")
	}
	encoded, err := json.Marshal(outputs)
	if err != nil || len(encoded) > maxPlacementOutputsJSON {
		return "", errors.New("selected destination has an invalid Mist output advertisement")
	}
	return string(encoded), nil
}
