package mist

import "encoding/json"

// StripLivepeerProcesses removes Livepeer process entries from a MistServer
// processes JSON array. Used when no Livepeer gateway is available in the cluster
// (self-hosted without transcoding). Audio transcode and thumbnail processes are kept.
func StripLivepeerProcesses(processesJSON string) string {
	var processes []map[string]interface{}
	if err := json.Unmarshal([]byte(processesJSON), &processes); err != nil {
		return processesJSON
	}
	var filtered []map[string]interface{}
	for _, p := range processes {
		if procType, ok := p["process"].(string); ok && procType == "Livepeer" {
			continue
		}
		filtered = append(filtered, p)
	}
	out, err := json.Marshal(filtered)
	if err != nil {
		return processesJSON
	}
	return string(out)
}
