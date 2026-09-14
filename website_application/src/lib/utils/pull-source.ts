export type PullSourcePlacementClass = "public" | "private";

const PRIVATE_UNICAST_HOST =
  /^[a-z][a-z0-9+.-]*:\/\/(?:[^@/]+@)?(?:10\.|172\.(?:1[6-9]|2[0-9]|3[01])\.|192\.168\.|\[?f[cd][0-9a-f]{2}:)/i;
const TSUDP_MULTICAST_HOST = /^tsudp:\/\/(?:[^@/]+@)?(?:22[4-9]|23[0-9])(?:\.|$)/i;

/**
 * Gives the form an early hint when the server will require explicit source
 * cluster pins. The backend classifier remains authoritative for supported
 * schemes, blocked ranges, and cluster capability checks.
 */
export function pullSourcePlacementClass(rawURI: string): PullSourcePlacementClass {
  const uri = rawURI.trim();
  return PRIVATE_UNICAST_HOST.test(uri) || TSUDP_MULTICAST_HOST.test(uri) ? "private" : "public";
}
