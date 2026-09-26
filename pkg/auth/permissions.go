package auth

import "strings"

// PermissionGranted reports whether an API token's granted scopes satisfy
// required. A resource's write scope includes its read scope: a token that may
// change streams may also read them.
func PermissionGranted(granted []string, required string) bool {
	required = strings.TrimSpace(required)
	if required == "" {
		return true
	}
	implied := ""
	if resource, ok := strings.CutSuffix(required, ":read"); ok {
		implied = resource + ":write"
	}
	for _, permission := range granted {
		permission = strings.TrimSpace(permission)
		if permission == required || (implied != "" && permission == implied) {
			return true
		}
	}
	return false
}
