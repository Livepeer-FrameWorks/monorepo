/**
 * Default structure descriptor for the vanilla player UI.
 *
 * This defines the layout of controls using blueprint type names.
 * Override by providing a custom structure in a SkinDefinition.
 */

import type { StructureDescriptor } from "./Blueprint";

export const DEFAULT_STRUCTURE: StructureDescriptor = {
  type: "container",
  children: [
    { type: "videocontainer" },
    { type: "loading" },
    { type: "error" },
    {
      type: "controls",
      children: [
        { type: "progress" },
        {
          type: "controlbar",
          children: [
            { type: "play" },
            { type: "seekBackward" },
            { type: "seekForward" },
            { type: "live" },
            { type: "currentTime" },
            { type: "spacer" },
            { type: "totalTime" },
            { type: "speaker" },
            { type: "volume" },
            { type: "settings" },
            { type: "pip" },
            { type: "fullscreen" },
          ],
        },
      ],
    },
  ],
};
