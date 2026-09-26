package updater

import (
	"context"
	"errors"
)

type systemdController struct{}

func (systemdController) SignalMistUSR1(ctx context.Context) error {
	command, args := linuxMistControllerSignalCommand()
	errService := runCommand(ctx, command, args...)
	if errService == nil {
		return nil
	}
	errProc := runCommand(ctx, "pkill", "-USR1", "-o", "-x", "MistController")
	if errProc == nil {
		return nil
	}
	return errors.Join(errService, errProc)
}

func linuxMistControllerSignalCommand() (string, []string) {
	return "systemctl", []string{"kill", "--kill-whom=main", "-s", "USR1", "frameworks-mistserver"}
}

func (systemdController) RestartCaddy(ctx context.Context) error {
	return runCommand(ctx, "systemctl", "restart", "frameworks-caddy")
}

// RestartMist prefers the unit restart and falls back to signaling Mist
// directly, which works without unit-management rights because Helmsman runs
// as Mist's user and the unit restarts itself (Restart=always).
func (systemdController) RestartMist(ctx context.Context) error {
	errService := runCommand(ctx, "systemctl", "restart", "frameworks-mistserver")
	if errService == nil {
		return nil
	}
	if errProc := restartMistControllerProcesses(ctx); errProc != nil {
		return errors.Join(errService, errProc)
	}
	return nil
}
