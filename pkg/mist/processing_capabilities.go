package mist

import (
	"context"
	"errors"
)

// ProcessingResourceControls reports only options the running Mist advertises.
func (c *Client) ProcessingResourceControls(ctx context.Context) (map[string]bool, error) {
	inventory, err := c.makeAPIRequestContext(ctx, map[string]any{"capabilities": true})
	if err != nil {
		return nil, err
	}
	capabilities, ok := inventory["capabilities"].(map[string]any)
	if !ok {
		return nil, errors.New("mist response has no capability inventory")
	}
	processes, ok := capabilities["processes"].(map[string]any)
	if !ok {
		return nil, errors.New("mist capability inventory has no processes")
	}
	controls := map[string]bool{}
	for process, keys := range map[string][]string{"AV": {"encode_threads", "decode_threads", "lookahead"}, "Thumbs": {"encode_threads", "decode_threads"}} {
		capability, ok := processes[process].(map[string]any)
		if !ok {
			controls[process] = false
			continue
		}
		optional, ok := capability["optional"].(map[string]any)
		if !ok {
			controls[process] = false
			continue
		}
		supported := true
		for _, key := range keys {
			if _, ok := optional[key]; !ok {
				supported = false
			}
		}
		controls[process] = supported
	}
	return controls, nil
}
