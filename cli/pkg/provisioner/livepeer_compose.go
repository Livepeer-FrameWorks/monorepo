package provisioner

import (
	"encoding/base64"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
)

// livepeerCompose is what a docker-mode go-livepeer service needs beyond the
// generic Compose render.
type livepeerCompose struct {
	volumes   []string
	stateDirs []map[string]string
	// secretFiles are project-relative files the role writes with mode 0600.
	secretFiles map[string]string
	// loopbackPorts are listeners published on host loopback alone.
	loopbackPorts []map[string]any
}

// applyLivepeerComposeEnv prepares env for a docker-mode go-livepeer service.
// The image's entrypoint is the livepeer binary with no arguments, and
// go-livepeer reads every flag from LP_<FLAG> (ff.WithEnvVarPrefix("LP")), so
// the flags the native unit passes as argv become LP_* variables. Inside the
// container a loopback http_addr or cli_addr would refuse the published
// port, so both bind every container interface on their configured port; the
// host publishes http_addr on loopback and the WireGuard address, as for
// every Compose service, and cli_addr on loopback alone. The state
// directories are mounted at their host paths, and the keystore is a 0600
// project file mounted read-only where keystore_path points.
func applyLivepeerComposeEnv(serviceName string, env map[string]string, stateDirs []string) (livepeerCompose, error) {
	out := livepeerCompose{secretFiles: map[string]string{}}
	if len(stateDirs) == 0 || stateDirs[0] == "" {
		return out, fmt.Errorf("%s: docker mode needs a state directory", serviceName)
	}
	stateDir := stateDirs[0]
	applyLivepeerNativeEnvDefaults(serviceName, env, stateDirs)
	for _, dir := range stateDirs {
		out.stateDirs = append(out.stateDirs, map[string]string{"path": dir, "owner": "root", "group": "root", "mode": "0700"})
	}
	out.volumes = append(out.volumes, stateDir+":"+stateDir)

	if env["keystore_path"] == "" && env["LIVEPEER_ETH_KEYSTORE_B64"] != "" {
		content, err := base64.StdEncoding.DecodeString(env["LIVEPEER_ETH_KEYSTORE_B64"])
		if err != nil {
			return out, fmt.Errorf("livepeer keystore: decode LIVEPEER_ETH_KEYSTORE_B64: %w", err)
		}
		keystorePath := filepath.Join(stateDir, "keystore", "key.json")
		out.secretFiles["keystore/key.json"] = string(content)
		out.volumes = append(out.volumes, "./keystore/key.json:"+keystorePath+":ro")
		env["keystore_path"] = keystorePath
	}
	delete(env, "LIVEPEER_ETH_KEYSTORE_B64")
	// ethPassword takes the password itself as well as a file path; the
	// Compose .env file holding it is mode 0600.
	if env["eth_password"] == "" && env["LIVEPEER_ETH_KEYSTORE_PASSWORD"] != "" {
		env["eth_password"] = env["LIVEPEER_ETH_KEYSTORE_PASSWORD"]
	}
	delete(env, "LIVEPEER_ETH_KEYSTORE_PASSWORD")
	applyLivepeerNativeSecretEnv(env)

	for _, key := range []string{"http_addr", "cli_addr"} {
		value := strings.TrimSpace(env[key])
		if value == "" {
			continue
		}
		_, port, err := net.SplitHostPort(value)
		if err != nil {
			return out, fmt.Errorf("%s: %s=%q is not host:port: %w", serviceName, key, value, err)
		}
		env[key] = net.JoinHostPort("0.0.0.0", port)
		if key == "cli_addr" {
			portNumber, convErr := strconv.Atoi(port)
			if convErr != nil {
				return out, fmt.Errorf("%s: cli_addr port %q: %w", serviceName, port, convErr)
			}
			out.loopbackPorts = append(out.loopbackPorts, map[string]any{"port": portNumber, "container_port": portNumber, "addrs": []string{"127.0.0.1"}})
		}
	}

	for _, arg := range livepeerNativeArgs(serviceName, env, stateDirs) {
		flag, value, hasValue := strings.Cut(strings.TrimPrefix(arg, "-"), "=")
		if !hasValue {
			value = "true"
		}
		env["LP_"+strings.ToUpper(flag)] = value
	}
	return out, nil
}

// livepeerHTTPPort is the port of a go-livepeer service's http_addr, or 0.
func livepeerHTTPPort(env map[string]string) int {
	_, port, err := net.SplitHostPort(strings.TrimSpace(env["http_addr"]))
	if err != nil {
		return 0
	}
	value, err := strconv.Atoi(port)
	if err != nil {
		return 0
	}
	return value
}
