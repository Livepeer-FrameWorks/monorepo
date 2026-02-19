import { useCallback, useEffect, useRef, useState } from "react";
import { Player } from "@livepeer-frameworks/player-react";
import { usePlayground } from "@/context/usePlayground";
import { ErrorBoundary } from "@/components/ErrorBoundary";
import { Button } from "@/components/ui/button";
import { loadMistPlayerScript, mountMistPlayer, destroyMistPlayer } from "@/lib/mist-player";

type ActivePlayer = "frameworks" | "mist";

const LS_KEY = "fw.player.playground.activePlayer";

function loadActivePlayer(): ActivePlayer {
  const stored = localStorage.getItem(LS_KEY);
  return stored === "mist" ? "mist" : "frameworks";
}

export function PlayerPreview() {
  const { streamName, thumbnailUrl, autoplayMuted, viewerBase, theme } = usePlayground();
  const [activePlayer, setActivePlayer] = useState<ActivePlayer>(loadActivePlayer);
  const [isLoaded, setIsLoaded] = useState(false);
  const [mistError, setMistError] = useState<string | null>(null);
  const mistContainerRef = useRef<HTMLDivElement>(null);
  const prevStreamRef = useRef(streamName);

  const doUnload = useCallback(() => {
    if (activePlayer === "mist" && mistContainerRef.current) {
      destroyMistPlayer(mistContainerRef.current);
    }
    setIsLoaded(false);
    setMistError(null);
  }, [activePlayer]);

  // Auto-unload when stream name changes + cleanup on unmount
  useEffect(() => {
    if (prevStreamRef.current !== streamName && isLoaded) {
      doUnload();
    }
    prevStreamRef.current = streamName;
    return () => {
      if (mistContainerRef.current) {
        destroyMistPlayer(mistContainerRef.current);
      }
    };
  }, [streamName, isLoaded, doUnload]);

  const switchPlayer = useCallback(
    (to: ActivePlayer) => {
      if (to === activePlayer) return;
      if (isLoaded) doUnload();
      setActivePlayer(to);
      localStorage.setItem(LS_KEY, to);
    },
    [activePlayer, isLoaded, doUnload]
  );

  function doLoad() {
    setMistError(null);
    if (activePlayer === "mist") {
      loadMistPlayerScript(viewerBase)
        .then(() => {
          setIsLoaded(true);
          // Mount after state update causes re-render
          requestAnimationFrame(() => {
            if (mistContainerRef.current) {
              try {
                mountMistPlayer(mistContainerRef.current, streamName, viewerBase);
              } catch (e) {
                setMistError(e instanceof Error ? e.message : "Failed to mount MistPlayer");
              }
            }
          });
        })
        .catch((e) => {
          setMistError(e instanceof Error ? e.message : "Failed to load player.js");
        });
    } else {
      setIsLoaded(true);
    }
  }

  return (
    <div className="slab slab--player flex-1">
      <div className="player-tab-bar">
        <button
          className={`player-tab${activePlayer === "frameworks" ? " active" : ""}`}
          onClick={() => switchPlayer("frameworks")}
        >
          FrameWorks
        </button>
        <button
          className={`player-tab${activePlayer === "mist" ? " active" : ""}`}
          onClick={() => switchPlayer("mist")}
        >
          MistPlayer
        </button>
      </div>

      <div className="slab-body--flush flex min-h-0 flex-col overflow-hidden">
        <div className="relative min-h-0 flex-1 overflow-hidden border-b border-border/30 bg-muted/40">
          {isLoaded && activePlayer === "frameworks" ? (
            <ErrorBoundary>
              <Player
                contentId={streamName}
                contentType="live"
                thumbnailUrl={thumbnailUrl || undefined}
                options={{
                  autoplay: autoplayMuted,
                  muted: autoplayMuted,
                  controls: true,
                  devMode: true,
                  debug: true,
                  mistUrl: viewerBase,
                  theme,
                }}
              />
            </ErrorBoundary>
          ) : isLoaded && activePlayer === "mist" ? (
            <div ref={mistContainerRef} className="h-full w-full" />
          ) : (
            <div className="absolute inset-0 flex flex-col items-center justify-center gap-2">
              <span className="text-sm text-muted-foreground">
                {mistError ? mistError : "Player unloaded"}
              </span>
              {mistError && (
                <Button variant="ghost" size="sm" onClick={doLoad}>
                  Retry
                </Button>
              )}
            </div>
          )}
        </div>
      </div>

      <div className="slab-actions slab-actions--row">
        <Button variant="ghost" onClick={doLoad} disabled={isLoaded}>
          Load
        </Button>
        <Button variant="ghost" onClick={doUnload} disabled={!isLoaded}>
          Unload
        </Button>
      </div>
    </div>
  );
}
