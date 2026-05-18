package resolvers

import "strings"

func artifactLifecycleStageIsTerminal(stage string) bool {
	switch strings.ToLower(strings.TrimSpace(stage)) {
	case "available", "completed", "complete", "done", "ready", "synced",
		"deleted", "evicted", "failed", "failed_terminal", "error", "lost_local":
		return true
	default:
		return false
	}
}

func artifactLifecycleStageCanOverrideRegistry(registryStage, lifecycleStage string) bool {
	registry := strings.ToLower(strings.TrimSpace(registryStage))
	lifecycle := strings.ToLower(strings.TrimSpace(lifecycleStage))
	if lifecycle == "" {
		return false
	}
	if registry != "" && artifactLifecycleStageIsTerminal(registry) && !artifactLifecycleStageIsTerminal(lifecycle) {
		return false
	}
	return true
}
