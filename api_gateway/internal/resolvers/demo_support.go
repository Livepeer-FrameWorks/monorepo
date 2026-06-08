package resolvers

import "fmt"

// errDemoUnavailable is the canonical response for a root field that has no demo
// representation. Returning it keeps demo-mode requests from falling through to a
// real backend (which, without a tenant, only yields an opaque transport/auth
// error) and gives the API explorer a clear, consistent reason. Fields that CAN
// be demoed return data from internal/demo instead.
func errDemoUnavailable(feature string) error {
	return fmt.Errorf("%s is not available in demo mode", feature)
}
