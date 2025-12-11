import type { ContentEndpoints } from "@livepeer-frameworks/player";
import { MistSettingsCard } from "./MistSettingsCard";
import { MistProfilesCard } from "./MistProfilesCard";
import { EndpointsPanel } from "./EndpointsPanel";
import { PlayerPreview } from "./PlayerPreview";
import { WhipPublisher } from "./WhipPublisher";
import type { MistSettings, MistProfile, MistContext, MistEndpointId, EndpointStatus } from "@/lib/types";

export type MistWorkspaceTabProps = {
  // Settings
  mistSettings: MistSettings;
  onSettingChange: (key: keyof MistSettings, value: string) => void;

  // Profiles
  mistProfiles: MistProfile[];
  selectedProfileId: string | null;
  onSelectProfile: (id: string | null) => void;
  onSaveProfile: () => void;
  onUpdateProfile: () => void;
  onDeleteProfile: () => void;
  onNewProfile: () => void;

  // Endpoints
  mistContext: MistContext;
  mistEndpoints: Record<MistEndpointId, string>;
  mistDefaultEndpoints: Record<MistEndpointId, string>;
  mistOverrides: Partial<Record<MistEndpointId, string>>;
  endpointStatus: Record<MistEndpointId, EndpointStatus>;
  copiedEndpoint: MistEndpointId | null;
  onEndpointChange: (id: MistEndpointId, value: string) => void;
  onEndpointReset: (id: MistEndpointId) => void;
  onCopyEndpoint: (id: MistEndpointId) => void;
  onCheckEndpoint: (id: MistEndpointId) => void;

  // Player
  showPlayer: boolean;
  networkOptIn: boolean;
  activeEndpoints: ContentEndpoints | null;
  contentId: string;
  contentType: "live" | "dvr" | "clip";
  thumbnailUrl: string;
  clickToPlay: boolean;
  autoplayMuted: boolean;
};

export function MistWorkspaceTab({
  mistSettings,
  onSettingChange,
  mistProfiles,
  selectedProfileId,
  onSelectProfile,
  onSaveProfile,
  onUpdateProfile,
  onDeleteProfile,
  onNewProfile,
  mistContext,
  mistEndpoints,
  mistDefaultEndpoints,
  mistOverrides,
  endpointStatus,
  copiedEndpoint,
  onEndpointChange,
  onEndpointReset,
  onCopyEndpoint,
  onCheckEndpoint,
  showPlayer,
  networkOptIn,
  activeEndpoints,
  contentId,
  contentType,
  thumbnailUrl,
  clickToPlay,
  autoplayMuted
}: MistWorkspaceTabProps) {
  return (
    <div className="workspace-layout">
      <div className="slab-stack">
        <MistSettingsCard
          settings={mistSettings}
          onSettingChange={onSettingChange}
        />

        <MistProfilesCard
          profiles={mistProfiles}
          selectedProfileId={selectedProfileId}
          onSelectProfile={onSelectProfile}
          onSaveProfile={onSaveProfile}
          onUpdateProfile={onUpdateProfile}
          onDeleteProfile={onDeleteProfile}
          onNewProfile={onNewProfile}
        />

        <div className="slab">
          <div className="slab-header">
            <h3 className="slab-title">WHIP Publish Helper</h3>
            <p className="slab-description">Push your webcam/mic directly into Mist without leaving the playground. Great for fast E2E checks.</p>
          </div>
          <WhipPublisher endpoint={mistEndpoints.whip} enabled={networkOptIn} />
        </div>
      </div>

      <div className="slab-stack">
        <EndpointsPanel
          mistContext={mistContext}
          mistEndpoints={mistEndpoints}
          mistDefaultEndpoints={mistDefaultEndpoints}
          mistOverrides={mistOverrides}
          endpointStatus={endpointStatus}
          copiedEndpoint={copiedEndpoint}
          networkOptIn={networkOptIn}
          onEndpointChange={onEndpointChange}
          onEndpointReset={onEndpointReset}
          onCopyEndpoint={onCopyEndpoint}
          onCheckEndpoint={onCheckEndpoint}
        />

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
    </div>
  );
}
