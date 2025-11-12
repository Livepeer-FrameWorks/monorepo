// Import CSS - rollup-plugin-postcss will inline and auto-inject
import "./styles/tailwind.css";

export { default as Player } from "./components/Player";
export { default as MistPlayer } from "./components/players/MistPlayer";
export { default as LoadingScreen } from "./components/LoadingScreen";
export { default as ThumbnailOverlay } from "./components/ThumbnailOverlay";
export { default as PlayerControls } from "./components/PlayerControls";
export * from "./components/Icons";

// Export types
export type {
  PlayerProps,
  MistPlayerProps,
  PlayerOptions,
  EndpointInfo,
  OutputEndpoint,
  ContentEndpoints,
  ContentMetadata,
  PlayerState,
  PlayerStateContext,
} from "./types"; 

export { ensurePlayerStyles, injectPlayerStyles } from "./styles";
export { globalPlayerManager, createPlayerManager, ensurePlayersRegistered } from "./core";
export type { StreamInfo, StreamSource } from "./core";
