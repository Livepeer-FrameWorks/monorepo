package provisioner

import (
	"context"

	"frameworks/cli/pkg/detect"
	"frameworks/cli/pkg/inventory"
)

// listmonkRoleVars turns the manifest's listmonk configuration into the
// variable surface frameworks.infra.listmonk expects. The role owns compose
// rendering via its own Jinja template; Go just hands over image, port, env.
func listmonkRoleVars(_ context.Context, _ inventory.Host, config ServiceConfig, _ RoleBuildHelpers) (map[string]any, error) {
	image := config.Image
	if image == "" {
		image = defaultListmonkImage
	}
	port := config.Port
	if port == 0 {
		port = 9001
	}
	return map[string]any{
		"listmonk_image": image,
		"listmonk_port":  port,
		"listmonk_env":   listmonkEnvMap(config),
	}, nil
}

func listmonkRoleDetect(_ context.Context, _ inventory.Host, _ RoleBuildHelpers) (*detect.ServiceState, error) {
	return &detect.ServiceState{Exists: false, Running: false}, nil
}

// listmonkEnvMap renders the LISTMONK_* env keys Listmonk consumes, sourced
// from the manifest's shared env_files via config.EnvVars. Empty values are
// omitted for optional keys (SMTP, frontend URL); required keys are always
// present even if empty so Listmonk's config parse is total.
func listmonkEnvMap(config ServiceConfig) map[string]any {
	dbUser := orElse(config.EnvVars["DATABASE_USER"], "postgres")
	env := map[string]any{
		"LISTMONK_app__address": "0.0.0.0:9000",
		"LISTMONK_db__host":     config.EnvVars["DATABASE_HOST"],
		"LISTMONK_db__port":     config.EnvVars["DATABASE_PORT"],
		"LISTMONK_db__user":     dbUser,
		"LISTMONK_db__password": config.EnvVars["DATABASE_PASSWORD"],
		"LISTMONK_db__database": "listmonk",
		"LISTMONK_db__ssl_mode": "disable",
	}
	if v := config.EnvVars["SMTP_HOST"]; v != "" {
		env["LISTMONK_app__smtp__host"] = v
	}
	if v := config.EnvVars["SMTP_PORT"]; v != "" {
		env["LISTMONK_app__smtp__port"] = v
	}
	if v := config.EnvVars["SMTP_USER"]; v != "" {
		env["LISTMONK_app__smtp__username"] = v
	}
	if v := config.EnvVars["SMTP_PASSWORD"]; v != "" {
		env["LISTMONK_app__smtp__password"] = v
	}
	env["LISTMONK_app__smtp__auth_protocol"] = "login"
	env["LISTMONK_app__smtp__tls_type"] = "STARTTLS"
	if v := config.EnvVars["FROM_EMAIL"]; v != "" {
		env["LISTMONK_app__from_email"] = v
	}
	return env
}
