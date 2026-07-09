package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
)

type launchdController struct{}

func (launchdController) SignalMistUSR1(ctx context.Context) error {
	errSystem := runCommand(ctx, "launchctl", "kill", "USR1", "system/com.livepeer.frameworks.mistserver")
	if errSystem == nil {
		return nil
	}
	errUser := runCommand(ctx, "launchctl", "kill", "USR1", fmt.Sprintf("gui/%d/com.livepeer.frameworks.mistserver", os.Getuid()))
	if errUser == nil {
		return nil
	}
	errProc := runCommand(ctx, "pkill", "-USR1", "-f", "MistController")
	if errProc == nil {
		return nil
	}
	return errors.Join(errSystem, errUser, errProc)
}

func (launchdController) RestartCaddy(ctx context.Context) error {
	return firstCommand(ctx,
		"launchctl", "kickstart", "-k", "system/com.livepeer.frameworks.caddy",
		"launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/com.livepeer.frameworks.caddy", os.Getuid()),
	)
}
