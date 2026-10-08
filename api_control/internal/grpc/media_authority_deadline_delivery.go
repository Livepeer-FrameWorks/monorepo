package grpc

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"frameworks/api_control/internal/database/commodoredb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/periodic"
)

// Short-lease authorities are valid for a minute or less and are renewed a
// third of the way through, so a renewal has at least twenty seconds to reach
// its cell. Delivery latency is bounded as follows:
//   - a delivery is claimed as soon as the publishing transaction commits, by
//     the replica that published it, when it has a free delivery slot, and
//     otherwise as soon as one frees;
//   - a failed delivery is claimed again at its next attempt time by the
//     replica that recorded the failure;
//   - anything else due (a lease left by a replica that stopped, work published
//     by a replica that stopped before claiming it) is found by the poll, at
//     most mediaAuthorityDeadlinePollMax plus its jitter after it became due.
//
// The poll backs off from mediaAuthorityDeadlinePollInterval to
// mediaAuthorityDeadlinePollMax while claims come back empty or fail, so idle
// replicas do not all read the queue every second, and every wait is jittered
// so replicas drift apart.
const (
	mediaAuthorityDeadlineDeliveryTimeout = 5 * time.Second
	mediaAuthorityDeadlineDeliveryLease   = 20 * time.Second
	// The claim costs a few index probes per cell it visits, tens of
	// milliseconds on YugabyteDB with a warm connection. The budget leaves room
	// for the engine retrying the statement after a conflict with another
	// replica's claim, and is small against the twenty seconds a renewal has.
	mediaAuthorityDeadlineClaimBudget  = 2 * time.Second
	mediaAuthorityDeadlinePollInterval = time.Second
	mediaAuthorityDeadlinePollMax      = 4 * time.Second
	// A claim that keeps failing is reported at warning level once when it
	// starts failing and then at most this often until it recovers.
	mediaAuthorityDeadlineFailureReportInterval = time.Minute
)

// mediaAuthorityWake carries a wake-up to a worker that sleeps between polls.
// Signals sent while the worker is busy collapse into one, which is enough:
// the worker claims everything due when it wakes.
type mediaAuthorityWake struct {
	once sync.Once
	ch   chan struct{}
}

func (w *mediaAuthorityWake) channel() chan struct{} {
	w.once.Do(func() { w.ch = make(chan struct{}, 1) })
	return w.ch
}

func (w *mediaAuthorityWake) notify() {
	select {
	case w.channel() <- struct{}{}:
	default:
	}
}

// notifyAt wakes the worker at a later time, when a delivery it rescheduled
// becomes due again.
func (w *mediaAuthorityWake) notifyAt(at time.Time) {
	time.AfterFunc(time.Until(at), w.notify)
}

// mediaAuthorityDeadlineDrain is what one pass over the short-lease queue did.
type mediaAuthorityDeadlineDrain struct {
	claimed int
	err     error
}

// mediaAuthorityDeadlinePoll decides when the short-lease worker claims next
// and what it reports about a failing claim.
type mediaAuthorityDeadlinePoll struct {
	delay        time.Duration
	failures     int
	failingSince time.Time
	reportedAt   time.Time
}

type mediaAuthorityDeadlineReportLevel int

const (
	mediaAuthorityDeadlineReportNone mediaAuthorityDeadlineReportLevel = iota
	// The claim failed and the failure is reported at warning level.
	mediaAuthorityDeadlineReportFailure
	// The claim failed again inside the report interval: debug level only.
	mediaAuthorityDeadlineReportRepeat
	// The claim succeeded after failing.
	mediaAuthorityDeadlineReportRecovered
)

type mediaAuthorityDeadlineReport struct {
	level      mediaAuthorityDeadlineReportLevel
	failures   int
	failingFor time.Duration
}

// next records a drain and returns how long to wait before the next claim.
// A drain that claimed something resets the wait, since more work tends to
// follow; an empty or failed drain doubles it up to the maximum.
func (p *mediaAuthorityDeadlinePoll) next(drain mediaAuthorityDeadlineDrain, now time.Time) (time.Duration, mediaAuthorityDeadlineReport) {
	var report mediaAuthorityDeadlineReport
	switch {
	case drain.err != nil:
		p.failures++
		if p.failures == 1 {
			p.failingSince = now
		}
		report.level = mediaAuthorityDeadlineReportRepeat
		if p.failures == 1 || now.Sub(p.reportedAt) >= mediaAuthorityDeadlineFailureReportInterval {
			p.reportedAt = now
			report.level = mediaAuthorityDeadlineReportFailure
		}
		report.failures, report.failingFor = p.failures, now.Sub(p.failingSince)
	case p.failures > 0:
		report = mediaAuthorityDeadlineReport{level: mediaAuthorityDeadlineReportRecovered, failures: p.failures, failingFor: now.Sub(p.failingSince)}
		p.failures, p.failingSince, p.reportedAt = 0, time.Time{}, time.Time{}
	}
	switch {
	case drain.err == nil && drain.claimed > 0:
		p.delay = mediaAuthorityDeadlinePollInterval
	case p.delay < mediaAuthorityDeadlinePollInterval:
		p.delay = mediaAuthorityDeadlinePollInterval
	default:
		p.delay = min(2*p.delay, mediaAuthorityDeadlinePollMax)
	}
	return p.delay, report
}

// runMediaAuthorityDeadlineDelivery delivers short-lease authorities until ctx
// ends: it drains the queue, then sleeps until a local publication or retry
// wakes it or the poll delay passes.
func (s *CommodoreServer) runMediaAuthorityDeadlineDelivery(ctx context.Context) {
	var poll mediaAuthorityDeadlinePoll
	sleep := func(delay time.Duration) {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-s.mediaAuthoritySchedule.deadlineWake.channel():
		case <-timer.C:
		}
	}
	sleep(periodic.StartOffset(mediaAuthorityDeadlinePollInterval, rand.Float64()))
	for ctx.Err() == nil {
		drain := s.processMediaAuthorityDeadlineDeliveryBatch(ctx)
		if ctx.Err() != nil {
			return
		}
		delay, report := poll.next(drain, time.Now())
		s.reportMediaAuthorityDeadlineClaim(drain, delay, report)
		sleep(periodic.Jittered(delay, periodic.DefaultJitter, rand.Float64()))
	}
}

func (s *CommodoreServer) reportMediaAuthorityDeadlineClaim(drain mediaAuthorityDeadlineDrain, delay time.Duration, report mediaAuthorityDeadlineReport) {
	if s.logger == nil || report.level == mediaAuthorityDeadlineReportNone {
		return
	}
	fields := logging.Fields{
		"consecutive_failures": report.failures,
		"failing_for":          report.failingFor.Round(time.Millisecond).String(),
		"next_claim_in":        delay.String(),
		"claim_budget":         mediaAuthorityDeadlineClaimBudget.String(),
	}
	switch report.level {
	case mediaAuthorityDeadlineReportFailure:
		fields["exceeded_budget"] = errors.Is(drain.err, context.DeadlineExceeded)
		s.logger.WithError(drain.err).WithFields(fields).Warn("Failed to claim short-lease media authority delivery")
	case mediaAuthorityDeadlineReportRepeat:
		s.logger.WithError(drain.err).WithFields(fields).Debug("Failed to claim short-lease media authority delivery")
	case mediaAuthorityDeadlineReportRecovered:
		s.logger.WithFields(fields).Info("Short-lease media authority delivery claims recovered")
	}
}

// mediaAuthorityDeadlineCursor is the cell the replica's last short-lease claim
// ended at; the next claim walks the cells from there.
type mediaAuthorityDeadlineCursor struct {
	mu   sync.Mutex
	cell string
}

// advance moves the cursor to the last claimed cell in walk order: the
// greatest claimed cell after the cursor, or when every claimed cell was
// reached by wrapping around, the greatest of those.
func (c *mediaAuthorityDeadlineCursor) advance(from string, rows []commodoredb.ClaimMediaAuthorityDeadlineDeliveryRow) {
	after, wrapped := "", ""
	for _, row := range rows {
		if row.CellID > from {
			after = max(after, row.CellID)
		} else {
			wrapped = max(wrapped, row.CellID)
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case after != "":
		c.cell = after
	case wrapped != "":
		c.cell = wrapped
	}
}

func (c *mediaAuthorityDeadlineCursor) load() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cell
}

// claimMediaAuthorityDeadlineDeliveries claims up to available short-lease
// deliveries inside the claim budget.
func (s *CommodoreServer) claimMediaAuthorityDeadlineDeliveries(ctx context.Context, available int) ([]commodoredb.ClaimMediaAuthorityDeadlineDeliveryRow, error) {
	claimCtx, cancel := context.WithTimeout(ctx, mediaAuthorityDeadlineClaimBudget)
	defer cancel()
	from := s.mediaAuthoritySchedule.deadlineCursor.load()
	rows, err := commodoredb.New(s.db).ClaimMediaAuthorityDeadlineDelivery(claimCtx, commodoredb.ClaimMediaAuthorityDeadlineDeliveryParams{
		LeaseMs: mediaAuthorityDeadlineDeliveryLease.Milliseconds(), AfterCell: from, BatchSize: int32(available),
	})
	if err != nil {
		if ctx.Err() == nil && claimCtx.Err() != nil {
			// The driver reports a cancelled statement in its own words; the
			// budget is what ended it.
			err = errors.Join(context.DeadlineExceeded, err)
		}
		return nil, err
	}
	s.mediaAuthoritySchedule.deadlineCursor.advance(from, rows)
	return rows, nil
}

// processMediaAuthorityDeadlineDeliveryBatch drains the short-lease queue: a
// single claimant feeds bounded delivery slots and claims again each time one
// frees, until nothing is claimable and no delivery is running. The database
// exposes one unlocked head per cell, so a slow cell neither multiplies idle
// claims nor blocks healthy cells.
func (s *CommodoreServer) processMediaAuthorityDeadlineDeliveryBatch(ctx context.Context) mediaAuthorityDeadlineDrain {
	var drain mediaAuthorityDeadlineDrain
	if s.db == nil || s.foghornPool == nil || s.quartermasterClient == nil {
		return drain
	}
	completed := make(chan struct{}, mediaAuthorityDeliveryWorkers)
	running := 0
	wait := func() {
		for running > 0 {
			<-completed
			running--
		}
	}
	for {
		if ctx.Err() != nil {
			wait()
			return drain
		}

		available := mediaAuthorityDeliveryWorkers - running
		if available == 0 {
			select {
			case <-completed:
				running--
			case <-ctx.Done():
			}
			continue
		}

		rows, err := s.claimMediaAuthorityDeadlineDeliveries(ctx, available)
		if err != nil {
			if ctx.Err() == nil {
				drain.err = err
			}
			wait()
			return drain
		}
		if len(rows) == 0 {
			if running == 0 {
				return drain
			}
			select {
			case <-completed:
				running--
			case <-ctx.Done():
			}
			continue
		}
		drain.claimed += len(rows)
		for _, claimed := range rows {
			row := commodoredb.ClaimMediaAuthorityDeliveriesRow(claimed)
			running++
			go func() {
				if retryAt := s.processMediaAuthorityDeliveryRow(ctx, row, mediaAuthorityDeadlineDeliveryTimeout); !retryAt.IsZero() {
					s.mediaAuthoritySchedule.deadlineWake.notifyAt(retryAt)
				}
				completed <- struct{}{}
			}()
		}
	}
}
