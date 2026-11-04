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
	client   control.PublisherLeaseReleaser
	logger   logging.Logger
	stop     chan struct{}
	wg       sync.WaitGroup
	isLeader func() bool
	recover  func(context.Context, control.PublisherLeaseReleaser) error
}

func NewPublisherAdmissionRecoveryJob(client control.PublisherLeaseReleaser, logger logging.Logger, isLeader func() bool) *PublisherAdmissionRecoveryJob {
	return &PublisherAdmissionRecoveryJob{client: client, logger: logger, stop: make(chan struct{}), isLeader: isLeader, recover: control.RecoverPublisherAdmissions}
}
func (j *PublisherAdmissionRecoveryJob) Start() {
	j.wg.Add(1)
	go func() {
		defer j.wg.Done()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			if err := j.reconcile(); err != nil {
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
		return nil
	}
	ctx, cancel := stoppable(j.stop, 5*time.Second)
	defer cancel()
	return j.recover(ctx, j.client)
}
