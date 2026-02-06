package notify

type PreferenceDefaults struct {
	Email     bool
	Websocket bool
	MCP       bool
}

func ResolvePreferences(defaults PreferenceDefaults, overrides *NotificationPreferences) PreferenceDefaults {
	if overrides == nil {
		return defaults
	}

	resolved := defaults
	if overrides.Email != nil {
		resolved.Email = *overrides.Email
	}
	if overrides.Websocket != nil {
		resolved.Websocket = *overrides.Websocket
	}
	if overrides.MCP != nil {
		resolved.MCP = *overrides.MCP
	}
	return resolved
}
