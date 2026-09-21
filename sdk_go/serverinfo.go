package frameworks

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strconv"
	"sync"
	"time"
)

type operationInfo struct {
	Kind  string
	Since string
}

// ServerStatus is what the client learned from the server's serverInfo.
type ServerStatus struct {
	// Version is the server's release version, or nil when the server
	// predates serverInfo.
	Version  *string
	Features []string
	// Verified is true when Version is a stable release (vMAJOR.MINOR.PATCH)
	// the client can compare. Development builds, git-describe builds, and
	// release candidates are unverified and pass every check.
	Verified bool
}

type semver struct{ major, minor, patch int }

var stableVersion = regexp.MustCompile(`^v?(\d+)\.(\d+)\.(\d+)$`)

func parseStableVersion(v string) (semver, bool) {
	m := stableVersion.FindStringSubmatch(v)
	if m == nil {
		return semver{}, false
	}
	var parts [3]int
	for i := range parts {
		n, err := strconv.Atoi(m[i+1])
		if err != nil {
			return semver{}, false
		}
		parts[i] = n
	}
	return semver{parts[0], parts[1], parts[2]}, true
}

func (a semver) less(b semver) bool {
	if a.major != b.major {
		return a.major < b.major
	}
	if a.minor != b.minor {
		return a.minor < b.minor
	}
	return a.patch < b.patch
}

// versionLess reports a < b when both are stable versions.
func versionLess(a, b string) bool {
	av, aok := parseStableVersion(a)
	bv, bok := parseStableVersion(b)
	return aok && bok && av.less(bv)
}

// probeTTL is how long a serverInfo answer is reused before the server is
// asked again.
const probeTTL = 5 * time.Minute

// probeEntry holds the probe result of one URL: an answer, or a 402. A
// probe that failed for any other reason leaves it empty so the next call
// probes again.
type probeEntry struct {
	lock   chan struct{}
	status *ServerStatus
	// payment is a 402 answer to serverInfo. It is no verdict, but it is
	// kept like an answer: a v0.3.10 gateway answers an anonymous serverInfo
	// with its x402 challenge because the field is not on its public
	// allowlist, and asking again on every call would answer the same.
	payment   error
	checkedAt time.Time
}

// probes caches serverInfo answers by GraphQL URL for every client in the
// process.
var probes sync.Map

// ServerInfo returns the server's version and features from the probe of
// its URL, which every client in the process shares for five minutes.
func (c *Client) ServerInfo(ctx context.Context) (*ServerStatus, error) {
	status, _, err := c.serverStatus(ctx, false)
	return status, err
}

// serverStatus returns the probe answer and whether it was reused from the
// cache. refresh asks the server even when a cached answer is still fresh.
func (c *Client) serverStatus(ctx context.Context, refresh bool) (*ServerStatus, bool, error) {
	v, _ := probes.LoadOrStore(c.url, &probeEntry{lock: make(chan struct{}, 1)})
	entry, ok := v.(*probeEntry)
	if !ok {
		return nil, false, errors.New("frameworks: serverInfo cache holds an unexpected value")
	}
	select {
	case entry.lock <- struct{}{}:
		defer func() { <-entry.lock }()
	case <-ctx.Done():
		return nil, false, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if !refresh && (entry.status != nil || entry.payment != nil) && c.now().Sub(entry.checkedAt) < probeTTL {
		return entry.status, true, entry.payment
	}
	entry.status, entry.payment = nil, nil
	data, err := c.send(ctx, "query", "ServerInfo", ServerInfo_Operation, nil)
	if err != nil {
		var mismatch *SchemaMismatchError
		if errors.As(err, &mismatch) {
			entry.status, entry.checkedAt = &ServerStatus{}, c.now()
			return entry.status, false, nil
		}
		var payment *PaymentRequiredError
		if errors.As(err, &payment) {
			entry.payment, entry.checkedAt = err, c.now()
		}
		return nil, false, err
	}
	var decoded ServerInfoResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil, false, &ProtocolError{ErrorInfo{Message: "decoding serverInfo: " + err.Error(), Cause: err}}
	}
	version := decoded.ServerInfo.Version
	_, verified := parseStableVersion(version)
	entry.status = &ServerStatus{Version: &version, Features: decoded.ServerInfo.Features, Verified: verified}
	entry.checkedAt = c.now()
	return entry.status, false, nil
}

func (c *Client) since(name string) string {
	if s, ok := c.operationSince[name]; ok {
		return s
	}
	return operations[name].Since
}

// gate checks the server before an operation. A failed probe (or a 402) is
// not a verdict: the operation proceeds and its own response decides the
// outcome. A cached answer that would fail the call is asked again first, so
// an upgraded server is seen at once.
func (c *Client) gate(ctx context.Context, name string) error {
	status, cached, err := c.serverStatus(ctx, false)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	verdict := c.check(status, name)
	if verdict == nil || !cached {
		return verdict
	}
	status, _, err = c.serverStatus(ctx, true)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return nil
	}
	return c.check(status, name)
}

// recheck handles an operation the server rejected as invalid against its
// schema: the server may have changed since the cached probe, so it is asked
// again at once. It returns the new verdict when the fresh answer fails the
// check, and mismatch otherwise.
func (c *Client) recheck(ctx context.Context, name string, mismatch error) error {
	status, _, err := c.serverStatus(ctx, true)
	if err != nil {
		return mismatch
	}
	if verdict := c.check(status, name); verdict != nil {
		return verdict
	}
	return mismatch
}

// check applies the line's minimum server and the operation's since to a
// probe answer.
func (c *Client) check(status *ServerStatus, name string) error {
	if status.Version == nil {
		return newServerTooOldError(nil, MinServerVersion)
	}
	if versionLess(*status.Version, MinServerVersion) {
		return newServerTooOldError(status.Version, MinServerVersion)
	}
	if since := c.since(name); status.Verified && since != "" && versionLess(*status.Version, since) {
		return &UnsupportedOperationError{
			ErrorInfo:     ErrorInfo{Message: name + " needs FrameWorks " + since + " or later; the server runs " + *status.Version},
			Operation:     name,
			Since:         since,
			ServerVersion: *status.Version,
		}
	}
	return nil
}
