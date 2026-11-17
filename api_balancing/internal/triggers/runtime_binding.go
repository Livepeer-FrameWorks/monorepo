package triggers

import (
	"context"
	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

func runtimeObservationAllowed(ctx context.Context, node, runtime string) (bool, error) {
	return control.StreamRegistryInstance.AcceptsNodeRuntime(ctx, mist.ExtractInternalName(runtime), node, runtime)
}

func runtimeTerminalObservationAllowed(ctx context.Context, node, runtime string) (bool, error) {
	return control.StreamRegistryInstance.AcceptsNodeRuntime(ctx, mist.ExtractInternalName(runtime), node, runtime, true)
}
