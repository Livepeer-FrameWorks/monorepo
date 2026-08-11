import { redirect } from "@sveltejs/kit";
import type { PageServerLoad } from "./$types";

// Playback QoE merged into the unified Player Experience page.
export const load: PageServerLoad = () => {
  redirect(301, "/analytics/player-experience");
};
