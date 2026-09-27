package config

import (
	"strconv"
	"strings"
	"sync/atomic"
)

// mistListenerPorts holds each Mist connector's configured listen port as the
// node's own Mist config reports it, keyed by upper-case connector name.
var mistListenerPorts atomic.Pointer[map[string]int]

// RecordMistListenerPorts replaces the listen-port snapshot with the protocols
// of a Mist config backup. A connector with a public address override is left
// out: its advertised URL, not its local listen port, is what clients dial.
func RecordMistListenerPorts(current map[string]any) {
	ports := map[string]int{}
	var protocols []any
	if cfg, ok := current["config"].(map[string]any); ok {
		if list, listOK := cfg["protocols"].([]any); listOK {
			protocols = list
		}
	}
	for _, raw := range protocols {
		protocol, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		connector, named := protocol["connector"].(string)
		if !named || connector == "" || hasPublicAddress(protocol["pubaddr"]) {
			continue
		}
		if port := listenerPort(protocol["port"]); port > 0 {
			ports[strings.ToUpper(connector)] = port
		}
	}
	mistListenerPorts.Store(&ports)
}

// MistListenerPort returns the configured listen port of a Mist connector, or
// 0 when Mist's config has not been read or does not set one.
func MistListenerPort(connector string) int {
	ports := mistListenerPorts.Load()
	if ports == nil {
		return 0
	}
	return (*ports)[strings.ToUpper(connector)]
}

func (m *Manager) configBackup() (map[string]any, error) {
	current, err := m.mistClient.ConfigBackup()
	if err == nil {
		RecordMistListenerPorts(current)
	}
	return current, err
}

func hasPublicAddress(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case []string:
		return len(typed) > 0
	default:
		return false
	}
}

func listenerPort(value any) int {
	var port int
	switch typed := value.(type) {
	case float64:
		if typed != float64(int(typed)) {
			return 0
		}
		port = int(typed)
	case int:
		port = typed
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(typed))
		if err != nil {
			return 0
		}
		port = parsed
	default:
		return 0
	}
	if port < 1 || port > 65535 {
		return 0
	}
	return port
}
