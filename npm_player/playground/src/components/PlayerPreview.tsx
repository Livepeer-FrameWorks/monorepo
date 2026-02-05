import { useMemo, useState } from "react";
import { Player } from "@livepeer-frameworks/player-react";
import { usePlayground } from "@/context/PlaygroundContext";
import { ErrorBoundary } from "@/components/ErrorBoundary";
import { Button } from "@/components/ui/button";
import { buildContentEndpointsFromSources } from "@/lib/mist-utils";

export function PlayerPreview() {
  const { streamName, playbackSources, thumbnailUrl, autoplayMuted, viewerBase } = usePlayground();
  const [isConnected, setIsConnected] = useState(false);

  const endpoints = useMemo(
    () => buildContentEndpointsFromSources(playbackSources, streamName, viewerBase),
    [playbackSources, streamName, viewerBase]
  );

  const showPlayer = isConnected;

  return (
    <div className="slab slab--player flex-1">
      <div className="slab-header">
        <h3 className="slab-title">Player</h3>
        <p className="slab-subtitle">Multi-protocol playback</p>
      </div>
      <div className="slab-body--flush flex flex-col">
        <div className="relative aspect-video overflow-hidden border-b border-border/30 bg-muted/40">
          {showPlayer ? (
            <ErrorBoundary>
              <Player
                contentId={streamName}
                contentType="live"
                endpoints={endpoints || undefined}
                thumbnailUrl={thumbnailUrl || undefined}
                options={{
                  autoplay: autoplayMuted,
                  muted: autoplayMuted,
                  controls: true,
                  devMode: true,
                  debug: true,
                  mistUrl: viewerBase, // Enable Direct Connect
                }}
              />
            </ErrorBoundary>
          ) : (
            <div className="flex h-full items-center justify-center">
              <span className="text-sm text-muted-foreground">Player unloaded</span>
            </div>
          )}
        </div>
      </div>
      <div className="slab-actions slab-actions--row">
        <Button variant="ghost" onClick={() => setIsConnected(true)} disabled={isConnected}>
          Load
        </Button>
        <Button variant="ghost" onClick={() => setIsConnected(false)} disabled={!isConnected}>
          Unload
        </Button>
      </div>
    </div>
  );
}
