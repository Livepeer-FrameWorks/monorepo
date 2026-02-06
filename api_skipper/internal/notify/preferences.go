package notify

type PreferenceDefaults struct {
	Email bool
	MCP   bool
}

func ResolvePreferences(defaults PreferenceDefaults, overrides *NotificationPreferences) PreferenceDefaults {
	if overrides == nil {
		return defaults
	}

	resolved := defaults
	if overrides.Email != nil {
		resolved.Email = *overrides.Email
	}
	if overrides.MCP != nil {
		resolved.MCP = *overrides.MCP
	}
	return resolved
}
