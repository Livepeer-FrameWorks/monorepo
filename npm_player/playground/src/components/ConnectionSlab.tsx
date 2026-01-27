import { usePlayground } from "@/context/PlaygroundContext";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

export function ConnectionSlab() {
  const { baseUrl, viewerPath, streamName, setBaseUrl, setViewerPath, setStreamName } =
    usePlayground();

  return (
    <div className="slab">
      <div className="slab-header">
        <h3 className="slab-title">Connection</h3>
      </div>
      <div className="slab-body--flush">
        <div className="slab-form-group">
          <Label htmlFor="base-url">Base URL</Label>
          <Input
            id="base-url"
            type="text"
            value={baseUrl}
            onChange={(e) => setBaseUrl(e.target.value)}
            placeholder="http://localhost:8080"
          />
        </div>
        <div className="slab-form-group">
          <Label htmlFor="viewer-path">Viewer path</Label>
          <Input
            id="viewer-path"
            type="text"
            value={viewerPath}
            onChange={(e) => setViewerPath(e.target.value)}
            placeholder="/view (optional)"
          />
        </div>
        <div className="slab-form-group">
          <Label htmlFor="stream-name">Stream name</Label>
          <Input
            id="stream-name"
            type="text"
            value={streamName}
            onChange={(e) => setStreamName(e.target.value)}
            placeholder="test"
          />
        </div>
      </div>
    </div>
  );
}
