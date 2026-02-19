import { usePlayground } from "@/context/usePlayground";

export function StreamsSlab() {
  const { activeStreams, streamName, setStreamName } = usePlayground();

  return (
    <div className="slab">
      <div className="slab-header">
        <h3 className="slab-title">Streams</h3>
      </div>
      <div className="slab-body--flush max-h-48 overflow-y-auto">
        {activeStreams.length === 0 ? (
          <div className="slab-form-group text-sm text-muted-foreground">No active streams</div>
        ) : (
          activeStreams.map((name) => (
            <div
              key={name}
              className={`stream-item${name === streamName ? " selected" : ""}`}
              onClick={() => setStreamName(name)}
            >
              <span className="text-sm font-medium">{name}</span>
              <span className="stream-badge">live</span>
            </div>
          ))
        )}
      </div>
    </div>
  );
}
