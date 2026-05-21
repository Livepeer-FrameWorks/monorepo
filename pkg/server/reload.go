package server

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// EnvFilePathFor returns the conventional native-mode env-file path the
// go_service Ansible role renders for a given service name. Matches
// `roles/go_service/defaults/main.yml: go_service_env_file:`.
func EnvFilePathFor(serviceName string) string {
	return "/etc/frameworks/" + serviceName + ".env"
}

// RegisterEnvFileReload wires a SIGHUP reload callback that re-reads the
// service's env file and applies any changes via config.ReloadFromFile.
// Logs the change set so operators can confirm a `systemctl reload` took
// effect, but never aborts on parse errors — a malformed env file would
// already have failed the role's validate step before deployment.
//
// Typical use: call once in main() before server.Start. Docker-mode
// deployments have no SIGHUP path so the callback never fires there; the
// registration is harmless.
func RegisterEnvFileReload(serviceName string, logger logging.Logger) {
	envPath := EnvFilePathFor(serviceName)
	if err := config.PrimeEnvFileOwnership(envPath); err != nil {
		logger.WithError(err).
			WithField("service", serviceName).
			WithField("env_file", envPath).
			Debug("env-file ownership prime skipped")
	}
	RegisterReload(func() error {
		res, err := config.ReloadFromFile(envPath)
		if err != nil {
			logger.WithError(err).
				WithField("service", serviceName).
				WithField("env_file", envPath).
				Warn("env-file reload failed")
			return err
		}
		if res.Empty() {
			logger.WithField("service", serviceName).Debug("env-file reload: no changes")
			return nil
		}
		logger.WithFields(logging.Fields{
			"service": serviceName,
			"added":   res.Added,
			"changed": res.Changed,
			"removed": res.Removed,
		}).Info("env-file reload applied")
		return nil
	})
}
