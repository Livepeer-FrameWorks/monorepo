package artifacts

import "strings"

// ParseEnvBytes parses env-file bytes into a key→value map. Blank lines
// and `#`-prefixed comment lines are skipped. The first `=` on a line
// splits key from value; subsequent `=` characters are kept in the
// value. Whitespace around keys and values is trimmed. Duplicate keys
// take the last-wins semantic.
func ParseEnvBytes(b []byte) map[string]string {
	out := make(map[string]string)
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rawKey, rawValue, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key := strings.TrimSpace(rawKey)
		if key == "" {
			continue
		}
		out[key] = strings.TrimSpace(rawValue)
	}
	return out
}
