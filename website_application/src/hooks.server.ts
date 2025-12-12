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

  return resolve(event);
};
