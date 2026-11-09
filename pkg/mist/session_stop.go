package mist

import (
	"context"
	"fmt"
)

// SessionsStopped checks current input, output and viewer counts. Buffered
// streams may remain available after their sessions have ended.
func (c *Client) SessionsStopped(ctx context.Context, names []string) (bool, error) {
	response, err := c.GetActiveStreamsFilteredContext(ctx, names)
	if err != nil {
		return false, fmt.Errorf("verify stopped sessions: %w", err)
	}
	raw, present := response["active_streams"]
	// Mist's long-form response is null when no stream matches the filter.
	if present && raw == nil {
		return true, nil
	}
	streams, ok := raw.(map[string]interface{})
	if !ok {
		return false, fmt.Errorf("verify stopped sessions: missing active_streams object")
	}
	for _, name := range names {
		raw, exists := streams[name]
		if !exists {
			continue
		}
		stream, valid := raw.(map[string]interface{})
		if !valid {
			return false, fmt.Errorf("verify stopped sessions: invalid stream %s", name)
		}
		for _, field := range []string{"viewers", "inputs", "outputs"} {
			count, validCount := stream[field].(float64)
			if !validCount || count < 0 {
				return false, fmt.Errorf("verify stopped sessions: invalid %s count for %s", field, name)
			}
			if count > 0 {
				return false, nil
			}
		}
	}
	return true, nil
}
