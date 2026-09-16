//go:build !linux

package updater

import "context"

// Linux production edges verify the controller's executable through /proc.
// launchd has no equivalent stable, unprivileged process-executable interface;
// its completion is covered by the existing fresh-heartbeat warmup probe.
func waitMistControllerReload(context.Context, string) error { return nil }
