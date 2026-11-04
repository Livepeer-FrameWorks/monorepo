// Initial camera framing for the network map: the clusters nearest the visitor,
// located by the browser's IANA timezone, or the densest cluster region when
// the timezone names no place. Nothing about the visitor leaves the browser.

const EARTH_RADIUS_KM = 6371;
// Clusters farther than this from the cluster nearest the visitor stay out of
// the frame, so one distant region does not pull the camera across an ocean.
const DEFAULT_REACH_KM = 4000;
const DEFAULT_NEAREST = 3;

function radians(degrees) {
  return (degrees * Math.PI) / 180;
}

export function distanceKm([lat1, lon1], [lat2, lon2]) {
  const dLat = radians(lat2 - lat1);
  const dLon = radians(lon2 - lon1);
  const a =
    Math.sin(dLat / 2) ** 2 +
    Math.cos(radians(lat1)) * Math.cos(radians(lat2)) * Math.sin(dLon / 2) ** 2;
  return 2 * EARTH_RADIUS_KM * Math.asin(Math.min(1, Math.sqrt(a)));
}

function clusterPoint(cluster) {
  const lat = Number(cluster?.latitude);
  const lon = Number(cluster?.longitude);
  if (!Number.isFinite(lat) || !Number.isFinite(lon) || (lat === 0 && lon === 0)) return null;
  return [lat, lon];
}

// timezoneOrigin returns the [lat, lon] of a timezone: its own reference point,
// or the centre of the zones in the same area for a name the table lacks (a
// link such as "Asia/Calcutta"). UTC, Etc/* and unknown areas return null.
export function timezoneOrigin(timeZone, table) {
  if (typeof timeZone !== "string" || !timeZone.includes("/")) return null;
  if (table[timeZone]) return table[timeZone];
  const area = timeZone.split("/")[0];
  if (area === "Etc") return null;
  const peers = Object.entries(table).filter(([zone]) => zone.startsWith(`${area}/`));
  if (!peers.length) return null;
  const lat = peers.reduce((sum, [, point]) => sum + point[0], 0) / peers.length;
  const lon = peers.reduce((sum, [, point]) => sum + point[1], 0) / peers.length;
  return [lat, lon];
}

export function browserTimeZone() {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || null;
  } catch {
    return null;
  }
}

function nearestPoints(points, origin, nearest, reachKm) {
  const ranked = points
    .map((point) => ({ point, distance: distanceKm(origin, point) }))
    .sort((left, right) => left.distance - right.distance)
    .map((entry) => entry.point);
  const anchor = ranked[0];
  return ranked.filter((point) => distanceKm(anchor, point) <= reachKm).slice(0, nearest);
}

function densestPoints(points, reachKm) {
  let best = [];
  for (const center of points) {
    const members = points.filter((point) => distanceKm(center, point) <= reachKm);
    if (members.length > best.length) best = members;
  }
  return best;
}

// frameClusters returns the [[west, south], [east, north]] bounds to open the
// map on, or null when no cluster has a location.
export function frameClusters(
  clusters,
  origin,
  { nearest = DEFAULT_NEAREST, reachKm = DEFAULT_REACH_KM } = {}
) {
  const points = (clusters || []).map(clusterPoint).filter(Boolean);
  if (!points.length) return null;
  const framed = origin
    ? nearestPoints(points, origin, nearest, reachKm)
    : densestPoints(points, reachKm);
  const lats = framed.map((point) => point[0]);
  const lons = framed.map((point) => point[1]);
  return [
    [Math.min(...lons), Math.min(...lats)],
    [Math.max(...lons), Math.max(...lats)],
  ];
}
