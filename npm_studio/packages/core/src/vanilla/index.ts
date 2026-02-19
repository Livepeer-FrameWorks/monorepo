/**
 * StreamCrafter Vanilla API
 * Export the StreamCrafter class for non-framework usage
 */

// V2 is now primary, V1 removed
export { StreamCrafterV2 as StreamCrafter } from "./StreamCrafterV2";
export { StreamCrafterV2 } from "./StreamCrafterV2";
export type { StreamCrafterV2Config as StreamCrafterConfig } from "./StreamCrafterV2";
export type { StreamCrafterV2Config } from "./StreamCrafterV2";

// Property-based facade
export { createStreamCrafter } from "./createStreamCrafter";
export type { CreateStreamCrafterConfig, StreamCrafterInstance } from "./createStreamCrafter";

// Reactive state
export { createStudioReactiveState } from "./StudioReactiveState";
export type { StudioReactiveState, StudioStateMap } from "./StudioReactiveState";
