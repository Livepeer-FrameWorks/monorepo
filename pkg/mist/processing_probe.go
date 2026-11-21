package mist

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

const (
	ProcessingSourceProbeParam     = "processing_source_probe"
	ProcessingSourceKeyParam       = "processing_source_key"
	ProcessingReservedMemoryBytes  = "processing_reserved_memory_bytes"
	ProcessingProtectedMemoryBytes = "processing_protected_memory_bytes"
)

type ProcessingSourceProbe struct {
	SourceKey string            `json:"source_key"`
	Source    SourceMediaInfo   `json:"source"`
	Outputs   map[string]string `json:"outputs"`
}

func ProcessingSourceIdentity(tenantID, jobID, artifactHash, source string) string {
	sum := sha256.Sum256([]byte(tenantID + "\x00" + jobID + "\x00" + artifactHash + "\x00" + source))
	return hex.EncodeToString(sum[:])
}

func DecodeProcessingSourceProbe(raw, key string) (ProcessingSourceProbe, error) {
	var probe ProcessingSourceProbe
	if err := json.Unmarshal([]byte(raw), &probe); err != nil {
		return probe, err
	}
	if len(probe.SourceKey) != 64 || probe.SourceKey != key {
		return probe, fmt.Errorf("processing probe source identity changed")
	}
	if probe.Source.Width < 0 || probe.Source.Height < 0 || probe.Source.Width > 32768 || probe.Source.Height > 32768 ||
		(probe.Source.VideoCodec != "" && (probe.Source.Width == 0 || probe.Source.Height == 0)) {
		return probe, fmt.Errorf("invalid cached source dimensions")
	}
	if probe.Outputs == nil {
		return probe, fmt.Errorf("processing probe lacks source metadata")
	}
	return probe, nil
}
