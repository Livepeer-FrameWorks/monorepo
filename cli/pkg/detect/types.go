package detect

import "time"

// ServiceState represents the detected state of a service
type ServiceState struct {
	ServiceName string            `json:"service_name"`
	Exists      bool              `json:"exists"`
	Mode        string            `json:"mode"` // "docker", "native", "yugabyte", "unknown"
	Running     bool              `json:"running"`
	Version     string            `json:"version,omitempty"`
	Healthy     bool              `json:"healthy"`
	Initialized bool              `json:"initialized"`  // Schema/topics created
	Reachable   bool              `json:"reachable"`    // Can we connect?
	ConfigMatch bool              `json:"config_match"` // Config matches desired state
	DetectedBy  string            `json:"detected_by"`  // Which method detected it
	Metadata    map[string]string `json:"metadata,omitempty"`
	DetectedAt  time.Time         `json:"detected_at"`
}

// DetectionResult contains results from a detection attempt
type DetectionResult struct {
	Method  string // inventory | docker | systemd | port | health | connection
	Success bool
	State   *ServiceState
	Error   error
}
