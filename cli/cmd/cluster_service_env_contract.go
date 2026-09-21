package cmd

import (
	"fmt"
	"strings"
	"sync"

	"frameworks/cli/internal/configschema"
	"frameworks/cli/pkg/inventory"
	"frameworks/cli/pkg/orchestrator"
	"frameworks/cli/pkg/provisioner"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/servicedefs"
)

var loadServiceConfigSchema = sync.OnceValues(configschema.Load)

// validateServiceEnvContract checks the final environment of one service
// process. The schema presence check runs in every profile, because a
// service refuses to start without its required keys in any environment.
// Keys listed in servicedefs.RequiredExternalEnv are left out: provision
// handles those through deferStartForMissingExternalEnv, which can install
// the service without starting it. The hand-written conditional rules
// (either/or keys, formats, production-only keys) run outside the dev profile.
func validateServiceEnvContract(manifest *inventory.Manifest, serviceID string, env map[string]string) error {
	schema, err := loadServiceConfigSchema()
	if err != nil {
		return err
	}
	external := map[string]bool{}
	for _, req := range servicedefs.RequiredExternalEnv(serviceID) {
		external[req.Key] = true
	}
	var missing []string
	for _, key := range schema.Missing(serviceID, env) {
		if !external[key] {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("service %s: missing required env var(s): %s", serviceID, strings.Join(missing, ", "))
	}
	if invalid := schema.Invalid(serviceID, env); len(invalid) > 0 {
		return fmt.Errorf("service %s: invalid env var(s): %s", serviceID, strings.Join(invalid, ", "))
	}
	return validateProductionServiceEnv(manifest, serviceID, env)
}

// validateTaskServiceEnvContract validates the env a built task config
// deploys, after the role-level additions the provisioner makes for
// task.Type. Every path that renders or deploys service env (provision,
// apply, diff, dry-run, restart, upgrade) calls it after buildTaskConfig.
// Infrastructure tasks carry no service env and are skipped.
func validateTaskServiceEnvContract(manifest *inventory.Manifest, task *orchestrator.Task, config provisioner.ServiceConfig) error {
	if manifest == nil || task == nil || task.Phase == orchestrator.PhaseInfrastructure {
		return nil
	}
	host, _ := manifest.GetHost(task.Host)
	env := provisioner.FinalServiceEnv(task.Type, host, config)
	return validateServiceEnvContract(manifest, task.Type, env)
}
