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

func metaStringSlice(m map[string]any, key string) []string {
	v, ok := m[key]
	if !ok {
		return nil
	}
	switch items := v.(type) {
	case []string:
		return items
	case []any:
		out := make([]string, 0, len(items))
		for _, item := range items {
			if s, ok := item.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
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

// metaBool returns a bool from Metadata, falling back to def when absent
// or of the wrong type.
func metaBool(m map[string]any, key string, def bool) bool {
	v, ok := m[key]
	if !ok {
		return def
	}
	if b, ok := v.(bool); ok {
		return b
	}
	return def
}
