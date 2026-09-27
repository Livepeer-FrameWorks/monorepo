package mist

import (
	"context"
	"fmt"
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
		"clients": map[string]interface{}{"fields": []string{"sessid", "host", "stream", "protocol"}},
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
