package wireguard

import "frameworks/pkg/mesh/wgpolicy"

// ValidateForApply enforces FrameWorks-specific mesh policy on a Config
// that is already type-valid. The rules live in pkg/mesh/wgpolicy so the
// runtime apply path and 'mesh doctor' enforce the same checks.
func ValidateForApply(cfg Config) error {
	return wgpolicy.ValidateForApply(cfg)
}
