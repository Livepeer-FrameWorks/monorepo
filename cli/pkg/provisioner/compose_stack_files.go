package provisioner

import (
	"encoding/base64"
	"fmt"
)

// composeStackFiles encodes the extra files a compose stack mounts for the
// compose_stack role, which decodes them with b64decode. Ansible templates
// every string variable, so a file containing Jinja syntax (a log4j pattern
// such as %notEmpty{%X}, a Caddy or Grafana template) would otherwise fail to
// render or be rewritten; decoded filter output is never templated again.
func composeStackFiles[V any](files map[string]V) map[string]string {
	encoded := make(map[string]string, len(files))
	for path, content := range files {
		encoded[path] = base64.StdEncoding.EncodeToString(fmt.Append(nil, content))
	}
	return encoded
}
