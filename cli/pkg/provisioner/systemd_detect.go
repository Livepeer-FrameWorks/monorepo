package provisioner

import (
	"context"
	"strconv"
	"strings"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/ssh"
)

func detectSystemdUnit(ctx context.Context, runner ssh.Runner, unit string) (*detect.ServiceState, error) {
	unit = strings.TrimSpace(unit)
	if unit == "" {
		return &detect.ServiceState{Exists: false, Running: false}, nil
	}
	result, err := runner.Run(ctx, "systemctl show --property=LoadState --property=ActiveState "+strconv.Quote(unit)+" 2>/dev/null")
	if err != nil {
		return nil, err
	}
	if result == nil {
		return &detect.ServiceState{Exists: false, Running: false}, nil
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(result.Stdout), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	exists := values["LoadState"] == "loaded"
	running := values["ActiveState"] == "active"
	return &detect.ServiceState{Exists: exists, Running: running}, nil
}
