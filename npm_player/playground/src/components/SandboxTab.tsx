import type { ContentEndpoints } from "@livepeer-frameworks/player";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { PlayerPreview } from "./PlayerPreview";
import type { MockStream } from "@/lib/types";

export type SandboxTabProps = {
  mockStreams: MockStream[];
  selectedMockId: string;
  onMockSelect: (id: string) => void;
  selectedMock: MockStream | undefined;
  thumbnailUrl: string;
  onThumbnailChange: (url: string) => void;
  clickToPlay: boolean;
  onClickToPlayChange: (enabled: boolean) => void;
  autoplayMuted: boolean;
  onAutoplayMutedChange: (enabled: boolean) => void;
  showPlayer: boolean;
  networkOptIn: boolean;
  activeEndpoints: ContentEndpoints | null;
  contentId: string;
  contentType: "live" | "dvr" | "clip";
};

export function SandboxTab({
  mockStreams,
  selectedMockId,
  onMockSelect,
  selectedMock,
  thumbnailUrl,
  onThumbnailChange,
  clickToPlay,
  onClickToPlayChange,
  autoplayMuted,
  onAutoplayMutedChange,
  showPlayer,
  networkOptIn,
  activeEndpoints,
  contentId,
  contentType
}: SandboxTabProps) {
  return (
    <div className="workspace-layout">
      <div className="slab">
        <div className="slab-header">
          <h3 className="slab-title">Choose a Fixture</h3>
          <p className="slab-description">Select from vetted public streams that stay off the FrameWorks balancers.</p>
        </div>
        <div className="slab-body--flush">
          <div className="slab-form-group">
            <Label htmlFor="mock-select">Fixture</Label>
            <select
              id="mock-select"
              className="mt-2 h-10 w-full border border-input bg-background px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2"
              value={selectedMockId}
              onChange={(event) => onMockSelect(event.target.value)}
            >
              {mockStreams.map((stream) => (
                <option key={stream.id} value={stream.id}>
                  {stream.label}
                </option>
              ))}
            </select>
          </div>
          <div className="slab-form-group bg-muted/40 text-sm text-muted-foreground">
            {selectedMock?.description}
          </div>
          <div className="seam" />
          <div className="slab-form-group">
            <Label htmlFor="thumbnail-url">Thumbnail image</Label>
            <Input
              id="thumbnail-url"
              className="mt-2"
              value={thumbnailUrl}
              onChange={(event) => onThumbnailChange(event.target.value)}
              placeholder="https://example.com/poster.jpg"
            />
            <p className="mt-1 text-xs text-muted-foreground">
              Applied as the poster image before playback. Works for both thumbnail overlay modes below.
            </p>
          </div>
          <div className="slab-form-group flex items-center justify-between">
            <Label htmlFor="click-to-play">Click to play</Label>
            <Switch id="click-to-play" checked={clickToPlay} onCheckedChange={onClickToPlayChange} />
          </div>
          <div className="slab-form-group flex items-center justify-between">
            <Label htmlFor="autoplay-muted">Autoplay muted</Label>
            <Switch id="autoplay-muted" checked={autoplayMuted} onCheckedChange={onAutoplayMutedChange} />
          </div>
        </div>
      </div>

      <PlayerPreview
        showPlayer={showPlayer}
        networkOptIn={networkOptIn}
        endpoints={activeEndpoints}
        contentId={contentId}
        contentType={contentType}
        thumbnailUrl={thumbnailUrl}
        clickToPlay={clickToPlay}
        autoplayMuted={autoplayMuted}
      />
    </div>
  );
}
