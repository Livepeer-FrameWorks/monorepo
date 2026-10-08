package networkobservation

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// Metric is the public projection of an observation. Stream correlation is kept separately.
type Metric struct {
	EventID           string    `json:"event_id"`
	Timestamp         time.Time `json:"timestamp"`
	Provider          string    `json:"provider"`
	Network           string    `json:"network"`
	Environment       string    `json:"environment"`
	ObserverID        string    `json:"observer_id,omitempty"`
	Region            string    `json:"region"`
	ProducerKind      string    `json:"producer_kind"`
	Kind              string    `json:"kind"`
	ResourceID        string    `json:"resource_id"`
	URI               string    `json:"uri"`
	IP                string    `json:"ip"`
	Workload          string    `json:"workload"`
	ProfileKey        string    `json:"profile_key"`
	Success           bool      `json:"success"`
	Qualifying        bool      `json:"qualifying"`
	FailureKind       string    `json:"failure_kind,omitempty"`
	Disposition       string    `json:"disposition,omitempty"`
	LatencyMS         uint32    `json:"latency_ms"`
	SourceDurationMS  uint32    `json:"source_duration_ms"`
	ResponseHeadersMS uint32    `json:"response_headers_ms"`
	ResponseBodyMS    uint32    `json:"response_body_ms"`
	Attempts          uint32    `json:"attempts"`
	FallbackCount     uint32    `json:"fallback_count"`
	DeadlineExceeded  bool      `json:"deadline_exceeded"`
	CadenceSeconds    uint32    `json:"cadence_seconds"`
	Price             *Quote    `json:"price,omitempty"`
}

func ProfileKey(profiles []string) string {
	copy := append([]string(nil), profiles...)
	sort.Strings(copy)
	return strings.Join(copy, ",")
}

type Cohort struct {
	Bucket           time.Time `json:"bucket"`
	Kind             string    `json:"kind"`
	ResourceID       string    `json:"resource_id"`
	IP               string    `json:"ip"`
	Region           string    `json:"region"`
	Workload         string    `json:"workload"`
	ProfileKey       string    `json:"profile_key"`
	Samples          uint64    `json:"samples"`
	Successes        uint64    `json:"successes"`
	Failures         uint64    `json:"failures"`
	QualifiedSamples uint64    `json:"qualified_samples"`
	P50MS            float64   `json:"p50_ms"`
	P95MS            float64   `json:"p95_ms"`
	P99MS            float64   `json:"p99_ms"`
	DurationSamples  uint64    `json:"duration_samples"`
	P95DurationRatio float64   `json:"p95_duration_ratio"`
	Retries          uint64    `json:"retries"`
	Fallbacks        uint64    `json:"fallbacks"`
	DeadlineMisses   uint64    `json:"deadline_misses"`
}
type History struct {
	RolledUp        bool              `json:"rolled_up"`
	Truncated       bool              `json:"truncated"`
	Summaries       []Cohort          `json:"summaries"`
	GeneratedAt     time.Time         `json:"generated_at"`
	Start           time.Time         `json:"start"`
	End             time.Time         `json:"end"`
	IntervalSeconds int               `json:"interval_seconds"`
	Cohorts         []Cohort          `json:"cohorts"`
	Observations    []Metric          `json:"observations"`
	Details         []json.RawMessage `json:"details,omitempty"`
}
