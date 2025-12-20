import { usePlayground } from "@/context/PlaygroundContext";
import { UriRow } from "./UriRow";

export function IngestUrisSlab() {
  const { ingestUris } = usePlayground();

  return (
    <div className="slab">
      <div className="slab-header">
        <h3 className="slab-title">Ingest URIs</h3>
      </div>
      <div className="slab-body--flush">
        <UriRow label="RTMP" uri={ingestUris.rtmp} />
        <UriRow label="SRT" uri={ingestUris.srt} />
        <UriRow label="WHIP" uri={ingestUris.whip} />
      </div>
    </div>
  );
}
