// Package composelog holds the container log policy every Compose file the
// CLI renders applies, matching the Docker daemon default the compose_stack
// role writes to /etc/docker/daemon.json. The Ansible compose templates carry
// the same literal values; TestComposeRenderersCapContainerLogs holds them in
// line.
package composelog

// Docker's local driver rotates and compresses on its own; max-size and
// max-file bound each container to 250 MB of logs.
const (
	Driver  = "local"
	MaxSize = "50m"
	MaxFile = "5"
)

// Block returns the service-level `logging:` stanza, each line prefixed with
// indent (the indentation of the service's own keys).
func Block(indent string) string {
	return indent + "logging:\n" +
		indent + "  driver: " + Driver + "\n" +
		indent + "  options:\n" +
		indent + "    max-size: \"" + MaxSize + "\"\n" +
		indent + "    max-file: \"" + MaxFile + "\"\n"
}

// Map returns the same stanza for renderers that marshal Compose from Go values.
func Map() map[string]any {
	return map[string]any{
		"driver": Driver,
		"options": map[string]any{
			"max-size": MaxSize,
			"max-file": MaxFile,
		},
	}
}
