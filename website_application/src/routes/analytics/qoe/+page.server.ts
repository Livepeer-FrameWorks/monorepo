import { redirect } from "@sveltejs/kit";
import type { PageServerLoad } from "./$types";

// Playback QoE merged into the Player Experience page (Playback tab).
export const load: PageServerLoad = () => {
  redirect(301, "/analytics/player-experience?tab=playback");
};
