import { Player, type ContentEndpoints } from "@livepeer-frameworks/player";
import { cn } from "@/lib/utils";

export type PlayerPreviewProps = {
  showPlayer: boolean;
  networkOptIn: boolean;
  endpoints: ContentEndpoints | null;
  contentId: string;
  contentType: "live" | "dvr" | "clip";
  thumbnailUrl: string;
  clickToPlay: boolean;
  autoplayMuted: boolean;
};

export function PlayerPreview({
  showPlayer,
  networkOptIn,
  endpoints,
  contentId,
  contentType,
  thumbnailUrl,
  clickToPlay,
  autoplayMuted
}: PlayerPreviewProps) {
  return (
    <div className="slab">
      <div className="slab-header">
        <h3 className="slab-title">Player Output</h3>
        <p className="slab-description">
          {showPlayer ? "Rendering with live player logic." : "Enable networking and configure an endpoint to mount the player."}
        </p>
      </div>
      <div className="slab-body--padded">
        <div className={cn("relative aspect-video overflow-hidden border border-border bg-muted", !showPlayer && "flex items-center justify-center")}>
          {showPlayer && endpoints ? (
            <Player
              contentId={contentId}
              contentType={contentType}
              endpoints={endpoints}
              thumbnailUrl={thumbnailUrl}
              options={{
                autoplay: autoplayMuted || !clickToPlay,
                muted: autoplayMuted,
                controls: true,
                stockControls: true
              }}
            />
          ) : (
            <span className="text-sm text-muted-foreground">
              {networkOptIn ? "Add a URL or choose a fixture to begin." : "Networking disabled. Toggle it on to exercise the player."}
            </span>
          )}
        </div>
      </div>
    </div>
  );
}
