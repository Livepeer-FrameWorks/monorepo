package wireguard

import (
	"fmt"
	"runtime"
)

// NewManager returns a platform-specific WireGuard manager
func NewManager(interfaceName string) (Manager, error) {
	switch runtime.GOOS {
	case "linux":
		return newLinuxManager(interfaceName), nil
	case "darwin":
		return newDarwinManager(interfaceName), nil
	default:
		return nil, fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}
