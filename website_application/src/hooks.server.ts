import type { Handle } from "@sveltejs/kit";
import { setSession } from "$houdini";

// Cookie names must match Go backend (api_gateway/internal/handlers/auth.go)
const ACCESS_TOKEN_COOKIE = "access_token";
const TENANT_ID_COOKIE = "tenant_id";

export const handle: Handle = async ({ event, resolve }) => {
  // Extract auth tokens from httpOnly cookies
  const accessToken = event.cookies.get(ACCESS_TOKEN_COOKIE);
  const tenantId = event.cookies.get(TENANT_ID_COOKIE);

  // Set Houdini session with auth data from cookies
  // This makes auth available to all Houdini queries during SSR and client-side
  setSession(event, {
    token: accessToken || null,
    tenantId: tenantId || null,
  });

  const response = await resolve(event);
  // The app domain owns no search surface (marketing/docs do); keep every page
  // crawlable-but-noindexed so already-indexed URLs get dropped by Google.
  response.headers.set("X-Robots-Tag", "noindex, nofollow");
  return response;
};
