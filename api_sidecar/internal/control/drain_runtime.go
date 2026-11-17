package control

import (
	"context"
	"fmt"
)

// isolatedRuntimeAbsent requires an authoritative inventory omission. A nuke
// acknowledgement means dispatch only; a stopping buffer still owns processes.
func isolatedRuntimeAbsent(ctx context.Context, client dvrSourceActiveStreamsReader, runtime string) (bool, error) {
	response, err := client.GetActiveStreamsFilteredContext(ctx, []string{runtime})
	if err != nil {
		return false, err
	}
	raw, present := response["active_streams"]
	if !present {
		return false, fmt.Errorf("runtime absence probe has no active_streams")
	}
	if raw == nil {
		return true, nil
	}
	streams, ok := raw.(map[string]interface{})
	if !ok {
		return false, fmt.Errorf("runtime absence probe has invalid active_streams")
	}
	_, present = streams[runtime]
	return !present, nil
}
