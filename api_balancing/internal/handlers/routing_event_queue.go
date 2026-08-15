package handlers

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// Every HTTP and gRPC routing decision is emitted through one bounded queue.
//
// The Decklog RPC is blocking, so during an outage a goroutine-per-event scheme
// accumulates blocked goroutines for as long as traffic continues — the
// resolution itself succeeds, so there is no backpressure to stop it. A fixed
// worker pool caps concurrent sends and gives each one a deadline; a full queue
// drops the event because routing telemetry is intentionally lossy.

const (
	routingEventQueueDepth = 1024
	routingEventWorkers    = 4
	// Bounding memory is not enough: without a deadline every worker can park
	// forever inside one stalled send, after which the queue fills and all
	// telemetry is dropped for the life of the process.
	routingEventSendTimeout = 5 * time.Second
)

type queuedRoutingEvent struct {
	client *decklog.BatchedClient
	event  *RoutingEvent
}

var (
	routingEventQueue   chan queuedRoutingEvent
	routingEventDropped atomic.Uint64

	routingEventQueueOnce sync.Once
)

// StartRoutingEventQueue launches the worker pool.
//
// Guarded rather than documented as call-once: a repeated Init would otherwise
// race on the channel and leak a second set of workers, and "production only
// calls this once" is an invariant nothing enforces.
func StartRoutingEventQueue() {
	routingEventQueueOnce.Do(startRoutingEventQueue)
}

func startRoutingEventQueue() {
	routingEventQueue = make(chan queuedRoutingEvent, routingEventQueueDepth)
	for range routingEventWorkers {
		go func() {
			for item := range routingEventQueue {
				sendQueuedRoutingEvent(item)
			}
		}()
	}
}

func sendQueuedRoutingEvent(item queuedRoutingEvent) {
	if item.event == nil {
		return
	}
	client := item.client
	if client == nil {
		client = decklogClient
	}
	if client == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), routingEventSendTimeout)
	defer cancel()

	if err := client.SendLoadBalancingContext(ctx, BuildLoadBalancingData(item.event)); err != nil {
		if metrics != nil && metrics.RoutingEventsShed != nil {
			metrics.RoutingEventsShed.WithLabelValues("send_failed").Inc()
		}
		if logger != nil {
			logger.WithFields(logging.Fields{
				"error": err,
				"node":  item.event.SelectedNode,
			}).Warn("Failed to send routing event to Decklog")
		}
	}
}

// EnqueueRoutingEvent submits an event from a caller that owns its Decklog
// client, such as the gRPC server.
func EnqueueRoutingEvent(client *decklog.BatchedClient, e *RoutingEvent) bool {
	if client == nil {
		return false
	}
	return enqueueRoutingEvent(client, e)
}

// enqueueRoutingEvent submits an event without blocking the caller. It reports
// whether the event was accepted; a false result means the queue was full and
// the event was dropped.
//
// client may be nil, in which case the package-level Decklog client is used.
func enqueueRoutingEvent(client *decklog.BatchedClient, e *RoutingEvent) bool {
	if e == nil || routingEventQueue == nil {
		// Queue not started (unit tests, early startup): drop rather than
		// spawn an unbounded goroutine.
		return false
	}
	select {
	case routingEventQueue <- queuedRoutingEvent{client: client, event: e}:
		return true
	default:
		dropped := routingEventDropped.Add(1)
		if metrics != nil && metrics.RoutingEventsShed != nil {
			metrics.RoutingEventsShed.WithLabelValues("queue_full").Inc()
		}
		// Log on a widening interval so a sustained outage does not turn a
		// telemetry drop into a log flood.
		if dropped == 1 || dropped%1000 == 0 {
			if logger != nil {
				logger.WithFields(logging.Fields{
					"dropped_total": dropped,
					"queue_depth":   routingEventQueueDepth,
				}).Warn("Routing event queue full; dropping telemetry")
			}
		}
		return false
	}
}

// RoutingEventsDropped reports how many events were shed because the queue was
// full. Operational visibility comes from the routing_events_shed_total metric;
// this accessor exists for tests.
func RoutingEventsDropped() uint64 { return routingEventDropped.Load() }
