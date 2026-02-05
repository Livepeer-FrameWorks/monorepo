import { usePlayground } from "@/context/PlaygroundContext";
import { UriRow } from "./UriRow";
import { Button } from "@/components/ui/button";
import { Loader2 } from "lucide-react";

export function PlaybackSourcesSlab() {
  const { playbackSources, playbackLoading, playbackError, pollSources } = usePlayground();

  return (
    <div className="slab slab--player">
      <div className="slab-header">
        <h3 className="slab-title">Playback Sources</h3>
      </div>
      <div className="slab-body--flush max-h-64 overflow-y-auto">
        {playbackError && (
          <div className="slab-form-group text-sm text-destructive">{playbackError}</div>
        )}
        {!playbackLoading && !playbackError && playbackSources.length === 0 && (
          <div className="slab-form-group text-sm text-muted-foreground">
            No sources. Press Poll to inspect playback sources, or just Connect to use the
            configured Base URL.
          </div>
        )}
        {playbackSources.map((source, i) => (
          <UriRow key={i} label={source.hrn} uri={source.url} />
        ))}
      </div>
      <div className="slab-actions">
        <Button variant="ghost" onClick={pollSources} disabled={playbackLoading}>
          {playbackLoading ? (
            <>
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              Polling…
            </>
          ) : (
            "Poll"
          )}
        </Button>
      </div>
    </div>
  );
}
