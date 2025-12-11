import { useMemo } from "react";
import { EndpointRow } from "./EndpointRow";
import { MIST_ENDPOINTS } from "@/lib/constants";
import type { MistEndpointId, MistContext, EndpointStatus } from "@/lib/types";

export type EndpointsPanelProps = {
  mistContext: MistContext;
  mistEndpoints: Record<MistEndpointId, string>;
  mistDefaultEndpoints: Record<MistEndpointId, string>;
  mistOverrides: Partial<Record<MistEndpointId, string>>;
  endpointStatus: Record<MistEndpointId, EndpointStatus>;
  copiedEndpoint: MistEndpointId | null;
  networkOptIn: boolean;
  onEndpointChange: (id: MistEndpointId, value: string) => void;
  onEndpointReset: (id: MistEndpointId) => void;
  onCopyEndpoint: (id: MistEndpointId) => void;
  onCheckEndpoint: (id: MistEndpointId) => void;
};

export function EndpointsPanel({
  mistContext,
  mistEndpoints,
  mistDefaultEndpoints,
  mistOverrides,
  endpointStatus,
  copiedEndpoint,
  networkOptIn,
  onEndpointChange,
  onEndpointReset,
  onCopyEndpoint,
  onCheckEndpoint
}: EndpointsPanelProps) {
  const ingestEndpointDefs = useMemo(() => MIST_ENDPOINTS.filter((def) => def.category === "ingest"), []);
  const playbackEndpointDefs = useMemo(() => MIST_ENDPOINTS.filter((def) => def.category === "playback"), []);

  return (
    <div className="slab">
      <div className="slab-header">
        <h3 className="slab-title">Derived Endpoints</h3>
        <p className="slab-description">
          Copy, tweak, and validate every ingest/egress URL. Use tokens like{" "}
          <code className="bg-muted px-1 font-mono text-xs">{'{stream}'}</code> to keep them dynamic.
        </p>
      </div>
      <div className="slab-body--flush">
        <div className="slab-form-group">
          <div className="flex items-center justify-between">
            <h4 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">Ingest</h4>
            <span className="text-xs text-muted-foreground">Feed Mist via OBS, FFmpeg, or browser capture.</span>
          </div>
        </div>
        {ingestEndpointDefs.map((def) => (
          <EndpointRow
            key={def.id}
            definition={def}
            value={mistOverrides[def.id] ?? mistDefaultEndpoints[def.id] ?? ""}
            resolvedValue={mistEndpoints[def.id]}
            isCustom={mistOverrides[def.id] !== undefined}
            onChange={(val) => onEndpointChange(def.id, val)}
            onReset={() => onEndpointReset(def.id)}
            onCopy={() => onCopyEndpoint(def.id)}
            status={endpointStatus[def.id]}
            showCheck={false}
            checking={endpointStatus[def.id] === "checking"}
            copied={copiedEndpoint === def.id}
            disabled={!mistEndpoints[def.id]}
            networkOptIn={networkOptIn}
          />
        ))}

        <div className="seam" />

        <div className="slab-form-group">
          <div className="flex items-center justify-between">
            <h4 className="text-sm font-semibold uppercase tracking-wide text-muted-foreground">Playback</h4>
            <span className="text-xs text-muted-foreground">Validate the outputs your apps and the new player will consume.</span>
          </div>
        </div>
        {playbackEndpointDefs.map((def) => (
          <EndpointRow
            key={def.id}
            definition={def}
            value={mistOverrides[def.id] ?? mistDefaultEndpoints[def.id] ?? ""}
            resolvedValue={mistEndpoints[def.id]}
            isCustom={mistOverrides[def.id] !== undefined}
            onChange={(val) => onEndpointChange(def.id, val)}
            onReset={() => onEndpointReset(def.id)}
            onCopy={() => onCopyEndpoint(def.id)}
            status={endpointStatus[def.id]}
            showCheck
            checking={endpointStatus[def.id] === "checking"}
            copied={copiedEndpoint === def.id}
            onCheck={() => onCheckEndpoint(def.id)}
            disabled={!mistEndpoints[def.id]}
            networkOptIn={networkOptIn}
          />
        ))}
      </div>
    </div>
  );
}
