package control

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	"google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
)

// controlEndpoint is one Foghorn instance the control client can dial. addr is
// the configured entry: it decides transport security, the TLS server name and
// the :authority of the connection. dial is the concrete address the
// connection goes to, one of the addresses addr resolved to.
type controlEndpoint struct {
	addr string
	dial string
}

const (
	// Bounds each dial so an instance that is down without refusing (a host that
	// is off, a dropped route) costs a few seconds before the next one is tried.
	controlConnectTimeout = 5 * time.Second
	// Bounds each name lookup made before a dial.
	controlResolveTimeout = 5 * time.Second
	// Longest pause before the client waits out its backoff.
	controlMaxBackoff = 30 * time.Second
)

// parseControlAddrs splits FOGHORN_CONTROL_ADDR into its entries. The value is
// one host:port or a comma-separated list of them, all instances of one cell.
func parseControlAddrs(raw string) []string {
	var addrs []string
	for entry := range strings.SplitSeq(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" || slices.Contains(addrs, entry) {
			continue
		}
		addrs = append(addrs, entry)
	}
	return addrs
}

type hostLookup func(ctx context.Context, host string) ([]string, error)

// resolveControlEndpoints expands every configured entry into one endpoint per
// address its name resolves to, so each instance behind a name is dialed on
// its own. It runs before every dial: the instances of a cell change while
// the node keeps running. An entry that does not resolve is kept as it is and
// the dial reports why.
func resolveControlEndpoints(ctx context.Context, addrs []string, lookup hostLookup) []controlEndpoint {
	var endpoints []controlEndpoint
	seen := map[string]bool{}
	add := func(endpoint controlEndpoint) {
		if seen[endpoint.dial] {
			return
		}
		seen[endpoint.dial] = true
		endpoints = append(endpoints, endpoint)
	}
	for _, addr := range addrs {
		host, port, err := net.SplitHostPort(addr)
		if err != nil || net.ParseIP(strings.Trim(host, "[]")) != nil {
			add(controlEndpoint{addr: addr, dial: addr})
			continue
		}
		lookupCtx, cancel := context.WithTimeout(ctx, controlResolveTimeout)
		ips, lookupErr := lookup(lookupCtx, host)
		cancel()
		if lookupErr != nil || len(ips) == 0 {
			add(controlEndpoint{addr: addr, dial: addr})
			continue
		}
		slices.Sort(ips)
		for _, ip := range ips {
			add(controlEndpoint{addr: addr, dial: net.JoinHostPort(ip, port)})
		}
	}
	return endpoints
}

// nextControlEndpoint picks the endpoint after the one dialed last, so a lost
// or refused connection moves on to another instance of the cell. The first
// dial starts at a random instance, which spreads a cell's nodes over its
// instances instead of piling them onto the first one listed.
func nextControlEndpoint(endpoints []controlEndpoint, lastDial string) controlEndpoint {
	for i, endpoint := range endpoints {
		if endpoint.dial == lastDial {
			return endpoints[(i+1)%len(endpoints)]
		}
	}
	return endpoints[rand.IntN(len(endpoints))]
}

// failoverDelay is the pause before dialing another instance after one was
// lost. It is short and random so the nodes of a cell that lost the same
// instance do not all dial the next one in the same millisecond.
func failoverDelay() time.Duration {
	return applyJitter(goingAwayRedialInterval/4, 100) / 2
}

// controlDialer keeps one control stream to the cell open. Every connection
// that ends, whether refused, lost or announced as going away, is followed by
// a prompt dial of the next instance; only once every instance has failed in a
// row does the ordinary backoff pace the next attempt.
type controlDialer struct {
	addrs   []string
	lookup  hostLookup
	connect func(controlEndpoint) error
	logger  logging.Logger
	// stop ends the loop between dials; nil runs it for the process lifetime.
	stop <-chan struct{}
	// lastDial is the address dialed last; the next dial goes to the one after
	// it. Empty starts at a random instance.
	lastDial string
}

func (d *controlDialer) sleep(delay time.Duration) bool {
	if d.stop == nil {
		time.Sleep(delay)
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-d.stop:
		return false
	}
}

func (d *controlDialer) run() {
	backoff := time.Second
	var goingAwayAt time.Time
	failuresInRow := 0
	for {
		if d.stop != nil {
			select {
			case <-d.stop:
				return
			default:
			}
		}
		endpoints := resolveControlEndpoints(context.Background(), d.addrs, d.lookup)
		if len(endpoints) == 0 {
			d.logger.Error("FOGHORN_CONTROL_ADDR names no Foghorn endpoint")
			if !d.sleep(controlMaxBackoff) {
				return
			}
			continue
		}
		endpoint := nextControlEndpoint(endpoints, d.lastDial)
		d.lastDial = endpoint.dial
		fields := logging.Fields{"foghorn_addr": endpoint.addr, "foghorn_dial": endpoint.dial}

		connStart := time.Now()
		err := d.connect(endpoint)
		announced := errors.Is(err, errFoghornGoingAway)
		switch {
		case announced:
			goingAwayAt = time.Now()
			backoff = time.Second
			failuresInRow = 0
			d.logger.WithFields(fields).Info("Foghorn is going away; reconnecting to another instance of the cell")
		case err != nil:
			d.logger.WithFields(fields).WithError(err).Warn("Helmsman control client disconnected; trying the next Foghorn instance")
		}
		if time.Since(connStart) > controlMaxBackoff {
			backoff = time.Second
			failuresInRow = 0
		}
		failuresInRow++
		if failuresInRow < len(endpoints) {
			if !d.sleep(failoverDelay()) {
				return
			}
			continue
		}
		failuresInRow = 0
		if !d.sleep(nextReconnectDelay(backoff, goingAwayAt, time.Now(), announced)) {
			return
		}
		// The backoff grows only while it is what paces the redial, so it starts
		// from a second again when the redial window after a notice runs out.
		redialing := !goingAwayAt.IsZero() && time.Since(goingAwayAt) < goingAwayRedialWindow
		if !redialing && backoff < controlMaxBackoff {
			backoff *= 2
		}
	}
}

// awaitControlChannelReady connects conn and waits until it is ready, failing
// as soon as the connection attempt fails or the timeout runs out. Without it
// opening the stream waits for gRPC's own connect deadline on an instance that
// does not answer.
func awaitControlChannelReady(conn *grpc.ClientConn, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	conn.Connect()
	for {
		state := conn.GetState()
		switch state {
		case connectivity.Ready:
			return nil
		case connectivity.TransientFailure, connectivity.Shutdown:
			return fmt.Errorf("connect to Foghorn: channel %s", state)
		}
		if !conn.WaitForStateChange(ctx, state) {
			return fmt.Errorf("connect to Foghorn: not ready after %s", timeout)
		}
	}
}
