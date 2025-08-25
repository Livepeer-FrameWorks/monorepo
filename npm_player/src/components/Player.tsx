import React, { useState } from "react";
import MistPlayer from "./MistPlayer";
import DirectSourcePlayer from "./DirectSourcePlayer";
import WHEPPlayer from "./WHEPPlayer";
import LoadingScreen from "./LoadingScreen";
import ThumbnailOverlay from "./ThumbnailOverlay";
import LogoOverlay from "./LogoOverlay";
import { PlayerProps, EndpointInfo, OutputEndpoint } from "../types";
import useViewerEndpoints from "../hooks/useViewerEndpoints";
// Use existing webapp header logo temporarily; will switch to SVG later
// Bundled via rollup url plugin
// eslint-disable-next-line @typescript-eslint/ban-ts-comment
// @ts-ignore
import defaultIcon from "../../public/icon.png";

const Player: React.FC<PlayerProps> = ({
  contentId,
  contentType,
  thumbnailUrl = null,
  options,
  endpoints
}) => {
  const [isPlaying, setIsPlaying] = useState<boolean>(false);
  const [isMuted, setIsMuted] = useState<boolean>(true);
  const supportsOverlay = false;

  const handlePlay = () => {
    setIsPlaying(true);
    setIsMuted(false);
  };

  // Show loading state while contacting load balancer
  // If endpoints not passed in, fetch via Gateway
  const gw = options?.gatewayUrl;
  const { endpoints: fetchedEndpoints, status: fetchStatus } = useViewerEndpoints(
    gw ? { gatewayUrl: gw, contentType, contentId, authToken: options?.authToken } : ({} as any)
  );
  const ep = endpoints?.primary ? endpoints : fetchedEndpoints || undefined;
  if (!ep?.primary) {
    const message = gw ? (fetchStatus === 'loading' ? 'Resolving viewing endpoint...' : 'Waiting for endpoint...') : 'Waiting for endpoint...';
    return <LoadingScreen message={message} />;
  }

  const primary: EndpointInfo = ep.primary as EndpointInfo;
  // Parse outputs as provided by Foghorn
  let htmlUrl: string | undefined = undefined;
  let whepUri: string | undefined = undefined;
  let directUrl: string | undefined = undefined;
  let playerJsUrl: string | undefined = undefined;
  if (primary.outputs) {
    const o = primary.outputs as Record<string, OutputEndpoint>;
    htmlUrl = o['MIST_HTML']?.url;
    playerJsUrl = o['PLAYER_JS']?.url;
    // WHEP is a special case: prefer explicit output, else derive from HTML only
    whepUri = o['WHEP']?.url;
    // Direct sources are small VOD files only (e.g., MP4/WEBM). HLS/DASH stay with Mist.
    directUrl = o['MP4']?.url || o['WEBM']?.url;
  } else {
    if (primary.protocol === 'MIST_HTML') htmlUrl = primary.url;
    if (primary.protocol === 'WHEP' || primary.protocol === 'webrtc') whepUri = primary.url;
    if (primary.protocol === 'MP4') directUrl = primary.url;
  }
  console.log("Player state:", { thumbnailUrl, supportsOverlay, isPlaying, isMuted, contentType });

  // Protocol selection scaffold with preferredProtocol and simple fallbacks
  const hasWhep = !!whepUri;
  const hasMist = !!htmlUrl;
  const hasDirect = !!directUrl;
  const preferred = options?.preferredProtocol || 'auto';

  const chooseMode = (): 'whep' | 'mist' | 'direct' => {
    switch (preferred) {
      case 'whep':
        if (hasWhep) return 'whep';
        if (hasMist) return 'mist';
        if (hasDirect) return 'direct';
        return contentType === 'live' ? 'whep' : (hasMist ? 'mist' : 'direct');
      case 'mist':
        if (hasMist) return 'mist';
        if (contentType === 'live' && hasWhep) return 'whep';
        if (hasDirect) return 'direct';
        return hasMist ? 'mist' : (hasWhep ? 'whep' : 'direct');
      case 'native':
        if (hasDirect) return 'direct';
        if (hasMist) return 'mist';
        if (hasWhep) return 'whep';
        return 'mist';
      case 'auto':
      default:
        if (contentType === 'live') {
          return hasWhep ? 'whep' : 'mist';
        }
        return hasDirect ? 'direct' : 'mist';
    }
  };
  const mode = chooseMode();

  // Render the appropriate player based on selected mode
  let playerComponent: React.ReactNode;
  if (mode === 'whep' && whepUri) {
    playerComponent = <WHEPPlayer key={`${contentId}-${(primary?.nodeId||'')}-whep`} whepUrl={whepUri} muted={isMuted} />;
  } else if (mode === 'direct' && directUrl) {
    playerComponent = (
      <DirectSourcePlayer
        key={`${contentId}-${(primary?.nodeId||'')}-direct`}
        src={directUrl}
        muted={isMuted}
        controls={true}
        poster={thumbnailUrl || undefined}
      />
    );
  } else {
    const mistPlayerProps: any = {
      key: `${contentId}-${(primary?.nodeId||'')}-mist`,
      streamName: contentId,
      htmlUrl,
      playerJsUrl,
      developmentMode: false,
    };
    if (thumbnailUrl) {
      mistPlayerProps.poster = thumbnailUrl;
    }
    playerComponent = <MistPlayer {...mistPlayerProps} />;
  }

  // Determine what overlay to show
  let overlayComponent: React.ReactNode = null;
  
  // Click-to-play mode: show thumbnail overlay when not playing
  if (thumbnailUrl && supportsOverlay && !isPlaying) {
    overlayComponent = (
      <ThumbnailOverlay
        thumbnailUrl={thumbnailUrl}
        onPlay={handlePlay}
        message={contentId}
        showUnmuteMessage={false}
        style={{
          position: "absolute",
          top: 0,
          left: 0,
          right: 0,
          bottom: 0,
          zIndex: 10
        }}
      />
    );
  }
  // Autoplay muted mode: show simple overlay when muted
  else if (supportsOverlay && isMuted && isPlaying) {
    overlayComponent = (
      <ThumbnailOverlay
        thumbnailUrl={undefined}
        onPlay={handlePlay}
        message={null}
        showUnmuteMessage={true}
        style={{
          position: "absolute",
          top: 0,
          left: 0,
          right: 0,
          bottom: 0,
          zIndex: 10
        }}
      />
    );
  }

  const branding = options?.branding || { showLogo: true };
  const resolvedLogo: string = options?.branding?.logoUrl || (defaultIcon as string);

  // Always render player, conditionally render overlay on top
  return (
    <div style={{ position: "relative", width: "100%", height: "100%" }}>
      {playerComponent}
      {overlayComponent}
      <LogoOverlay
        src={resolvedLogo}
        show={branding.showLogo !== false}
        position={branding.position}
        width={branding.width}
        height={branding.height}
        clickUrl={branding.clickUrl}
      />
    </div>
  );
};

export default Player; 