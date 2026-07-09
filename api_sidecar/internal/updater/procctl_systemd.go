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
	errProc := runCommand(ctx, "pkill", "-USR1", "-f", "MistController")
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
