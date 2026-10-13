package coordinator

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
)

// CatalogRefreshTimeout bounds one contract catalog refresh.
const CatalogRefreshTimeout = 2 * time.Minute

// KafkaSampleInterval is how often the observer samples its consumer group.
const KafkaSampleInterval = 30 * time.Second

// Readiness reports whether the collector works, not only whether its
// database answers. Every window derives from the catalog refresh interval:
//   - startup grace: one refresh interval plus one refresh timeout, so the
//     first refresh and its retry can complete before "never" is reported;
//   - catalog staleness: two intervals plus one timeout, so one failed refresh
//     between two successful ones never flips readiness;
//   - Kafka staleness: two intervals, the window in which a failed sample, a
//     rebalance without partitions or a lag that is not being consumed is
//     tolerated.
//
// Until the catalog has refreshed once and the consumer group has been
// sampled with partitions, the observer is not ready.
type Readiness struct {
	mu              sync.Mutex
	catalogInterval time.Duration
	started         time.Time

	catalogSuccess time.Time
	catalogError   string

	kafka           KafkaSample
	kafkaSampledAt  time.Time
	kafkaError      string
	kafkaAssignedAt time.Time
	kafkaProgressAt time.Time
	kafkaStopped    string
}

// KafkaSample is one observation of the observer's consumer group.
type KafkaSample struct {
	// Joined reports whether this process is a member of the group.
	Joined bool
	// Partitions counts the group's assigned partitions across members.
	Partitions int
	// Lag sums the assigned partitions' known lag.
	Lag int64
	// Committed sums the assigned partitions' committed offsets.
	Committed int64
	Err       error
}

// ReadinessReport is the /ready response body.
type ReadinessReport struct {
	Ready   bool     `json:"ready"`
	Reasons []string `json:"reasons"`
	Catalog struct {
		LastSuccess *time.Time `json:"last_success,omitempty"`
		LastError   string     `json:"last_error,omitempty"`
	} `json:"catalog"`
	Kafka struct {
		Partitions   int        `json:"partitions"`
		Lag          int64      `json:"lag"`
		LastSample   *time.Time `json:"last_sample,omitempty"`
		LastProgress *time.Time `json:"last_progress,omitempty"`
		LastError    string     `json:"last_error,omitempty"`
	} `json:"kafka"`
}

func NewReadiness(catalogInterval time.Duration, started time.Time) *Readiness {
	return &Readiness{catalogInterval: catalogInterval, started: started}
}

func (r *Readiness) grace() time.Duration { return r.catalogInterval + CatalogRefreshTimeout }
func (r *Readiness) catalogStale() time.Duration {
	return 2*r.catalogInterval + CatalogRefreshTimeout
}
func (r *Readiness) kafkaStale() time.Duration { return 2 * r.catalogInterval }

// CatalogRefreshed records one refresh; err covers reading and persisting it.
func (r *Readiness) CatalogRefreshed(at time.Time, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err != nil {
		r.catalogError = err.Error()
		return
	}
	r.catalogSuccess = at
	r.catalogError = ""
}

// KafkaSampled records one consumer-group sample. Committed offsets that
// advance, or no lag at all, count as progress; a changed partition set
// restarts the progress window because its offset sum is not comparable.
func (r *Readiness) KafkaSampled(at time.Time, s KafkaSample) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.Err != nil {
		r.kafkaError = s.Err.Error()
		return
	}
	r.kafkaError = ""
	if s.Joined && s.Partitions > 0 {
		r.kafkaAssignedAt = at
	}
	if r.kafkaSampledAt.IsZero() || s.Lag == 0 || s.Partitions != r.kafka.Partitions || s.Committed > r.kafka.Committed {
		r.kafkaProgressAt = at
	}
	r.kafka = s
	r.kafkaSampledAt = at
}

// KafkaStopped records that the consumer loop returned while the observer runs.
func (r *Readiness) KafkaStopped(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.kafkaStopped = "stopped"
	if err != nil {
		r.kafkaStopped = err.Error()
	}
}

func (r *Readiness) Check(now time.Time) ReadinessReport {
	r.mu.Lock()
	defer r.mu.Unlock()
	report := ReadinessReport{Reasons: []string{}}
	starting := now.Sub(r.started) <= r.grace()
	withError := func(reason, err string) string {
		if err != "" {
			return reason + ": " + err
		}
		return reason
	}

	switch {
	case r.catalogSuccess.IsZero() && starting:
		report.Reasons = append(report.Reasons, withError("catalog: waiting for the first contract catalog refresh", r.catalogError))
	case r.catalogSuccess.IsZero():
		report.Reasons = append(report.Reasons, withError("catalog: no successful contract catalog refresh since start", r.catalogError))
	case now.Sub(r.catalogSuccess) > r.catalogStale():
		report.Reasons = append(report.Reasons, withError(fmt.Sprintf("catalog: no successful refresh since %s", r.catalogSuccess.Format(time.RFC3339)), r.catalogError))
	}

	switch {
	case r.kafkaStopped != "":
		report.Reasons = append(report.Reasons, "kafka: consumer stopped: "+r.kafkaStopped)
	case r.kafkaSampledAt.IsZero() && starting:
		report.Reasons = append(report.Reasons, withError("kafka: waiting for the first consumer group sample", r.kafkaError))
	case r.kafkaSampledAt.IsZero():
		report.Reasons = append(report.Reasons, withError("kafka: consumer group never sampled since start", r.kafkaError))
	case now.Sub(r.kafkaSampledAt) > r.kafkaStale():
		report.Reasons = append(report.Reasons, withError(fmt.Sprintf("kafka: consumer group not sampled since %s", r.kafkaSampledAt.Format(time.RFC3339)), r.kafkaError))
	case r.kafkaAssignedAt.IsZero() && starting:
		report.Reasons = append(report.Reasons, "kafka: waiting to join the consumer group with assigned partitions")
	case r.kafkaAssignedAt.IsZero():
		report.Reasons = append(report.Reasons, "kafka: never joined the consumer group with assigned partitions")
	case now.Sub(r.kafkaAssignedAt) > r.kafkaStale():
		report.Reasons = append(report.Reasons, fmt.Sprintf("kafka: not a member of the consumer group with assigned partitions since %s", r.kafkaAssignedAt.Format(time.RFC3339)))
	case r.kafka.Lag > 0 && now.Sub(r.kafkaProgressAt) > r.kafkaStale():
		report.Reasons = append(report.Reasons, fmt.Sprintf("kafka: lag of %d records without committed progress since %s", r.kafka.Lag, r.kafkaProgressAt.Format(time.RFC3339)))
	}

	report.Ready = len(report.Reasons) == 0
	if !r.catalogSuccess.IsZero() {
		at := r.catalogSuccess
		report.Catalog.LastSuccess = &at
	}
	report.Catalog.LastError = r.catalogError
	report.Kafka.Partitions, report.Kafka.Lag, report.Kafka.LastError = r.kafka.Partitions, r.kafka.Lag, r.kafkaError
	if !r.kafkaSampledAt.IsZero() {
		sampled, progress := r.kafkaSampledAt, r.kafkaProgressAt
		report.Kafka.LastSample, report.Kafka.LastProgress = &sampled, &progress
	}
	return report
}

// GroupLagReader is the slice of kadm.Client the Kafka sample uses.
type GroupLagReader interface {
	Lag(ctx context.Context, groups ...string) (kadm.DescribedGroupLags, error)
}

// SampleKafkaGroup describes group and sums its assigned partitions' lag and
// committed offsets. memberID is this process's group member ID; empty means
// it has not joined.
func SampleKafkaGroup(ctx context.Context, reader GroupLagReader, group, memberID string) KafkaSample {
	lags, err := reader.Lag(ctx, group)
	if err != nil {
		return KafkaSample{Err: err}
	}
	described, ok := lags[group]
	if !ok {
		return KafkaSample{Err: fmt.Errorf("consumer group %s was not described", group)}
	}
	if err := described.Error(); err != nil {
		return KafkaSample{Err: err}
	}
	var sample KafkaSample
	for _, member := range described.Members {
		if memberID != "" && member.MemberID == memberID {
			sample.Joined = true
		}
	}
	for _, partitions := range described.Lag {
		for _, partition := range partitions {
			if partition.IsEmpty() {
				continue
			}
			sample.Partitions++
			if partition.Err != nil || partition.Lag < 0 {
				continue
			}
			sample.Lag += partition.Lag
			if partition.Commit.At > 0 {
				sample.Committed += partition.Commit.At
			}
		}
	}
	return sample
}
