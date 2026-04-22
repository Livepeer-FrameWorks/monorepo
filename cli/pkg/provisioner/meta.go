package provisioner

// metaString reads a string from a ServiceConfig.Metadata map.
// Returns "" when the key is absent or of the wrong type.
func metaString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// metaIntOr returns an int from Metadata, falling back to def when absent
// or of the wrong type.
func metaIntOr(m map[string]any, key string, def int) int {
	v, ok := m[key]
	if !ok {
		return def
	}
	if n, ok := v.(int); ok {
		return n
	}
	return def
}
