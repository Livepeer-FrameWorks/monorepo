// Package ownership keeps open incidents with the tenant that owns their
// cluster: it reacts to Quartermaster cluster_created and cluster_updated
// service events and reconciles every open incident once at startup.
package ownership

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"frameworks/api_incidents/internal/incidents"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

const (
	// ConsumerGroup is the competing consumer group every Lookout replica joins
	// on the aggregator service_events topic.
	ConsumerGroup = "lookout-cluster-ownership"

	eventClusterCreated = "cluster_created"
	eventClusterUpdated = "cluster_updated"

	sourceEvent   = "event"
	sourceStartup = "startup"

	defaultRetryBase = time.Second
	defaultRetryMax  = 30 * time.Second
)

// Reconciler moves open incidents to their cluster's verified owner.
type Reconciler interface {
	ReconcileClusterScope(ctx context.Context, clusterID string) (int, error)
	ReconcileOpenIncidentScopes(ctx context.Context) (int, error)
}

// Consumer applies cluster ownership changes to open incidents.
type Consumer struct {
	Reconciler Reconciler
	Logger     logging.Logger
	Metrics    *incidents.Metrics
	RetryBase  time.Duration
	RetryMax   time.Duration
	// Sleep waits between retries; it returns early with ctx's error.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Handle applies one service event. cluster_created and cluster_updated are
// acted on: Quartermaster emits them from its outbox when a cluster is
// registered or its owner or class may have changed. An alert can precede the
// cluster's registration and open a platform incident, so creation is
// reconciled like an update. The owner is re-read from Quartermaster rather
// than taken from the event. Undecodable events and events without a cluster
// ID are skipped. An error means the change was not applied.
func (c *Consumer) Handle(ctx context.Context, msg kafka.Message) error {
	var event kafka.ServiceEvent
	if err := json.Unmarshal(msg.Value, &event); err != nil {
		c.Metrics.ObserveOwnershipReconcile(sourceEvent, "invalid")
		c.warn(err, msg, "Skipping undecodable service event")
		return nil
	}
	if event.EventType != eventClusterCreated && event.EventType != eventClusterUpdated {
		return nil
	}
	clusterID := clusterIDOf(event)
	if clusterID == "" {
		c.Metrics.ObserveOwnershipReconcile(sourceEvent, "invalid")
		c.warn(errors.New(event.EventType+" without cluster_id"), msg, "Skipping cluster event")
		return nil
	}
	moved, err := c.Reconciler.ReconcileClusterScope(ctx, clusterID)
	if err != nil {
		c.Metrics.ObserveOwnershipReconcile(sourceEvent, failureResult(err))
		return err
	}
	c.Metrics.ObserveOwnershipReconcile(sourceEvent, resultFor(moved))
	if moved > 0 && c.Logger != nil {
		c.Logger.WithField("cluster_id", clusterID).WithField("incidents", moved).Info("Moved open incidents after cluster ownership change")
	}
	return nil
}

// HandleUntilApplied is the handler registered on the consumer. pkg/kafka does
// not redeliver a failed record to a running consumer, so the change is
// retried here with backoff until it applies; the partition does not advance
// and nothing is committed meanwhile. It returns an error only when ctx ends.
func (c *Consumer) HandleUntilApplied(ctx context.Context, msg kafka.Message) error {
	for attempt := 1; ; attempt++ {
		err := c.Handle(ctx, msg)
		if err == nil {
			return nil
		}
		delay := c.backoff(attempt)
		if c.Logger != nil {
			c.Logger.WithError(err).WithFields(logging.Fields{
				"topic":     msg.Topic,
				"partition": msg.Partition,
				"offset":    msg.Offset,
				"attempt":   attempt,
				"retry_in":  delay.String(),
			}).Warn("Cluster ownership change not applied; retrying")
		}
		if sleepErr := c.sleep(ctx, delay); sleepErr != nil {
			return errors.Join(err, sleepErr)
		}
	}
}

// ReconcileAtStartup reconciles every open incident with Quartermaster,
// retrying with backoff until every cluster's owner was verified or ctx ends.
// It covers ownership changes made while no Lookout consumed service events.
func (c *Consumer) ReconcileAtStartup(ctx context.Context) {
	for attempt := 1; ; attempt++ {
		moved, err := c.Reconciler.ReconcileOpenIncidentScopes(ctx)
		if err == nil {
			c.Metrics.ObserveOwnershipReconcile(sourceStartup, resultFor(moved))
			if c.Logger != nil {
				c.Logger.WithField("incidents", moved).Info("Startup incident ownership reconcile complete")
			}
			return
		}
		c.Metrics.ObserveOwnershipReconcile(sourceStartup, failureResult(err))
		delay := c.backoff(attempt)
		if c.Logger != nil {
			c.Logger.WithError(err).WithField("attempt", attempt).WithField("retry_in", delay.String()).
				Warn("Startup incident ownership reconcile incomplete; retrying")
		}
		if c.sleep(ctx, delay) != nil {
			return
		}
	}
}

func clusterIDOf(event kafka.ServiceEvent) string {
	if id, ok := event.Data["cluster_id"].(string); ok && strings.TrimSpace(id) != "" {
		return strings.TrimSpace(id)
	}
	if event.ResourceType == "cluster" {
		return strings.TrimSpace(event.ResourceID)
	}
	return ""
}

func resultFor(moved int) string {
	if moved > 0 {
		return "rescoped"
	}
	return "unchanged"
}

func failureResult(err error) string {
	if errors.Is(err, incidents.ErrScopeUnverified) {
		return "unverified"
	}
	return "error"
}

func (c *Consumer) backoff(attempt int) time.Duration {
	base, maxDelay := c.RetryBase, c.RetryMax
	if base <= 0 {
		base = defaultRetryBase
	}
	if maxDelay <= 0 {
		maxDelay = defaultRetryMax
	}
	delay := base
	for i := 1; i < attempt && delay < maxDelay; i++ {
		delay *= 2
	}
	return min(delay, maxDelay)
}

func (c *Consumer) sleep(ctx context.Context, d time.Duration) error {
	if c.Sleep != nil {
		return c.Sleep(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Consumer) warn(err error, msg kafka.Message, text string) {
	if c.Logger == nil {
		return
	}
	c.Logger.WithError(err).WithFields(logging.Fields{
		"topic":     msg.Topic,
		"partition": msg.Partition,
		"offset":    msg.Offset,
	}).Warn(text)
}
