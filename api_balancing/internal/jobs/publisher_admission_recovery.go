package jobs

import (
	"context"
	"sync"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// PublisherAdmissionRecoveryJob settles durable publisher executions and gives
// back only their owner-fenced leases, without delaying an edge response.
type PublisherAdmissionRecoveryJob struct {
	client      control.PublisherLeaseReleaser
	logger      logging.Logger
	stop        chan struct{}
	wg          sync.WaitGroup
	isLeader    func() bool
	recover     func(context.Context, control.PublisherLeaseReleaser) error
	now         func() time.Time
	nextAttempt time.Time
	backoff     time.Duration
	lastWarning time.Time
}

func NewPublisherAdmissionRecoveryJob(client control.PublisherLeaseReleaser, logger logging.Logger, isLeader func() bool) *PublisherAdmissionRecoveryJob {
	return &PublisherAdmissionRecoveryJob{client: client, logger: logger, stop: make(chan struct{}), isLeader: isLeader, recover: control.RecoverPublisherAdmissions, now: time.Now}
}
func (j *PublisherAdmissionRecoveryJob) Start() {
	j.wg.Add(1)
	go func() {
		defer j.wg.Done()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			if err := j.reconcile(); err != nil && j.shouldWarn() {
				j.logger.WithError(err).Warn("Publisher admission recovery failed; durable obligations remain pending")
			}
			select {
			case <-j.stop:
				return
			case <-tick.C:
			}
		}
	}()
}
func (j *PublisherAdmissionRecoveryJob) Stop() { close(j.stop); j.wg.Wait() }

func (j *PublisherAdmissionRecoveryJob) reconcile() error {
	if j.isLeader != nil && !j.isLeader() {
		j.nextAttempt, j.backoff, j.lastWarning = time.Time{}, 0, time.Time{}
		control.ClearPublisherRecoveryBacklog()
		return nil
	}
	if j.now().Before(j.nextAttempt) {
		return nil
	}
	ctx, cancel := stoppable(j.stop, 5*time.Second)
	defer cancel()
	err := j.recover(ctx, j.client)
	if err == nil {
		j.nextAttempt, j.backoff, j.lastWarning = time.Time{}, 0, time.Time{}
		return nil
	}
	if j.backoff == 0 {
		j.backoff = time.Second
	} else {
		j.backoff = min(j.backoff*2, 15*time.Second)
	}
	j.nextAttempt = j.now().Add(j.backoff)
	return err
}

func (j *PublisherAdmissionRecoveryJob) shouldWarn() bool {
	now := j.now()
	if !j.lastWarning.IsZero() && now.Sub(j.lastWarning) < time.Minute {
		return false
	}
	j.lastWarning = now
	return true
}
