package system

import "fmt"

// DockerCommand returns a shell command that runs `docker <args>` as the SSH
// user when that user can reach the Docker daemon, and otherwise through
// passwordless sudo. The provisioning user on managed hosts is usually not in
// the docker group, so the socket refuses it.
//
// args is shell text that follows `docker` (already quoted by the caller). The
// daemon reachability probe selects exactly one invocation, so a real failure is
// reported once with its own stderr instead of being retried under sudo. The
// result is a single `if ...; fi` compound command: pipes and redirects appended
// by the caller apply to whichever branch ran.
func DockerCommand(args string) string {
	return fmt.Sprintf("if docker version >/dev/null 2>&1; then docker %s; else sudo -n docker %s; fi", args, args)
}
