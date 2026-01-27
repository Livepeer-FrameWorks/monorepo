/**
 * WHIP Publisher - StreamCrafter Integration
 *
 * Uses the self-contained <StreamCrafter /> component.
 * Just pass the WHIP endpoint and get full UI with camera/screen controls.
 */

import { useState } from "react";
import { usePlayground } from "@/context/PlaygroundContext";
import { StreamCrafter } from "@livepeer-frameworks/streamcrafter-react";

type RendererType = "auto" | "webgpu" | "webgl" | "canvas2d";

export function WhipPublisher() {
  const { ingestUris } = usePlayground();
  const endpoint = ingestUris.whip;
  const [renderer, setRenderer] = useState<RendererType>("auto");
  const [key, setKey] = useState(0); // Force remount when renderer changes

  const handleRendererChange = (newRenderer: RendererType) => {
    setRenderer(newRenderer);
    setKey((k) => k + 1); // Remount StreamCrafter to apply new renderer
  };

  return (
    <div className="slab flex-1">
      <div className="slab-header">
        <h3 className="slab-title">StreamCrafter</h3>
        <div className="flex items-center gap-2">
          <span className="text-xs text-tn-fg-dark">Renderer:</span>
          <select
            value={renderer}
            onChange={(e) => handleRendererChange(e.target.value as RendererType)}
            className="px-2 py-1 text-xs bg-tn-bg-dark border border-tn-fg-gutter/30 rounded text-tn-fg"
          >
            <option value="auto">Auto</option>
            <option value="webgpu">WebGPU</option>
            <option value="webgl">WebGL</option>
            <option value="canvas2d">Canvas2D</option>
          </select>
        </div>
      </div>

      <div className="slab-body--flush">
        <StreamCrafter
          key={key}
          whipUrl={endpoint || undefined}
          initialProfile="broadcast"
          devMode={true}
          debug={true}
          enableCompositor={true}
          compositorConfig={{ renderer }}
          onStateChange={(state, context) => {
            console.debug("[StreamCrafter] State:", state, context);
          }}
          onError={(error) => {
            console.error("[StreamCrafter] Error:", error);
          }}
        />
      </div>
    </div>
  );
}
