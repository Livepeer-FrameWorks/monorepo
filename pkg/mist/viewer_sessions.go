package mist

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// InvalidateSessionIDs re-runs USER_NEW for exactly the listed Mist sessions
// (`invalidate_sessid`). The sessions keep playing while the trigger runs; a
// refusal ends them.
func (c *Client) InvalidateSessionIDs(ctx context.Context, sessionIDs []string) error {
	if len(sessionIDs) == 0 {
		return nil
	}
	_, err := c.makeAPIRequestContext(ctx, map[string]interface{}{"invalidate_sessid": sessionIDs})
	return err
}

// SessionStreamNames returns the exact Mist stream names whose sessions belong
// to the named streams, for stop_sessions, which matches names exactly. A name
// that carries a runtime prefix is kept as given; a bare internal name also
// covers the push (live+) and pull (pull+) runtime names a live stream runs
// under. Every stream Mist lists a viewer session of under the same internal
// name is added, so a runtime name the edge chose is covered too.
func SessionStreamNames(names []string, live []ViewerSession) []string {
	var out []string
	add := func(name string) {
		if name != "" && !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	bares := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		add(name)
		bare := ExtractInternalName(name)
		if bare == name {
			add("live+" + bare)
			add("pull+" + bare)
		}
		bares = append(bares, bare)
	}
	for _, session := range live {
		if slices.Contains(bares, ExtractInternalName(session.Stream)) {
			add(session.Stream)
		}
	}
	return out
}

// ViewerSession is one live Mist viewer session from the `clients` API.
type ViewerSession struct {
	SessionID string
	Host      string
	Stream    string
	Protocol  string
}

// ViewerSessions lists Mist's current viewer sessions. Input and output
// sessions (session ids prefixed I and O) are left out.
func (c *Client) ViewerSessions(ctx context.Context) ([]ViewerSession, error) {
	response, err := c.makeAPIRequestContext(ctx, map[string]interface{}{
		// Without a time Mist reports the sessions of ten minutes ago; -5 asks
		// for those active five seconds ago, as the stats poller does.
		"clients": map[string]interface{}{"time": -5, "fields": []string{"sessid", "host", "stream", "protocol"}},
	})
	if err != nil {
		return nil, fmt.Errorf("clients query failed: %w", err)
	}
	return parseViewerSessions(response)
}

func parseViewerSessions(response map[string]interface{}) ([]ViewerSession, error) {
	clients, ok := response["clients"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("clients response missing")
	}
	fields, ok := clients["fields"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("clients response lacks its field list")
	}
	index := map[string]int{}
	for i, field := range fields {
		if name, isName := field.(string); isName {
			index[name] = i
		}
	}
	for _, name := range []string{"sessid", "host", "stream", "protocol"} {
		if _, present := index[name]; !present {
			return nil, fmt.Errorf("clients response lacks field %q", name)
		}
	}
	// Mist sends null data when there are no sessions.
	rows, ok := clients["data"].([]interface{})
	if !ok {
		rows = nil
	}
	sessions := make([]ViewerSession, 0, len(rows))
	for _, raw := range rows {
		row, ok := raw.([]interface{})
		if !ok {
			continue
		}
		cell := func(name string) string {
			i := index[name]
			if i >= len(row) {
				return ""
			}
			if value, ok := row[i].(string); ok {
				return value
			}
			return ""
		}
		session := ViewerSession{SessionID: cell("sessid"), Host: cell("host"), Stream: cell("stream"), Protocol: cell("protocol")}
		if session.SessionID == "" || session.Stream == "" || strings.HasPrefix(session.SessionID, "I") || strings.HasPrefix(session.SessionID, "O") {
			continue
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}
