export type LatLng = [number, number];

const EARTH_KM = 6371;
const GREAT_CIRCLE_THRESHOLD_KM = 3000;

/**
 * Samples a curved path between two lat/lng points. Short hops use a quadratic
 * Bezier with a perpendicular control-point lift; long hops use great-circle
 * slerp on the unit sphere for the familiar "flight path" look.
 */
export function samplePath(from: LatLng, to: LatLng, samples = 32): LatLng[] {
  if (haversineKm(from, to) > GREAT_CIRCLE_THRESHOLD_KM) {
    return greatCirclePath(from, to, Math.max(samples, 48));
  }
  return bezierArcPath(from, to, samples);
}

/**
 * Interpolates a single point along the same curve at parametric position `t`
 * (0..1). Used by pulse animations so the moving dot rides the visible arc.
 */
export function pointOnPath(from: LatLng, to: LatLng, t: number): LatLng {
  if (haversineKm(from, to) > GREAT_CIRCLE_THRESHOLD_KM) {
    return slerp(from, to, t);
  }
  return bezierAt(from, to, t);
}

function bezierArcPath(from: LatLng, to: LatLng, samples: number): LatLng[] {
  const out: LatLng[] = new Array(samples + 1);
  for (let i = 0; i <= samples; i++) {
    out[i] = bezierAt(from, to, i / samples);
  }
  return out;
}

function bezierAt(from: LatLng, to: LatLng, t: number): LatLng {
  const [c1, c2] = bezierControl(from, to);
  const u = 1 - t;
  const lat = u * u * from[0] + 2 * u * t * c1 + t * t * to[0];
  const lng = u * u * from[1] + 2 * u * t * c2 + t * t * to[1];
  return [lat, lng];
}

function bezierControl(from: LatLng, to: LatLng): [number, number] {
  const mLat = (from[0] + to[0]) / 2;
  const mLng = (from[1] + to[1]) / 2;
  const dLat = to[0] - from[0];
  const dLng = to[1] - from[1];
  // Rotate the chord by -90° on screen (north is up); lift magnitude scales
  // with chord length so short links bow gently and long chords arc more.
  const lift = 0.15;
  return [mLat + dLng * lift, mLng - dLat * lift];
}

function greatCirclePath(from: LatLng, to: LatLng, samples: number): LatLng[] {
  const out: LatLng[] = new Array(samples + 1);
  for (let i = 0; i <= samples; i++) {
    out[i] = slerp(from, to, i / samples);
  }
  return out;
}

function slerp(from: LatLng, to: LatLng, t: number): LatLng {
  const a = toCartesian(from);
  const b = toCartesian(to);
  const dot = Math.max(-1, Math.min(1, a[0] * b[0] + a[1] * b[1] + a[2] * b[2]));
  const omega = Math.acos(dot);
  if (omega < 1e-6) return from;
  const sinO = Math.sin(omega);
  const ka = Math.sin((1 - t) * omega) / sinO;
  const kb = Math.sin(t * omega) / sinO;
  const x = ka * a[0] + kb * b[0];
  const y = ka * a[1] + kb * b[1];
  const z = ka * a[2] + kb * b[2];
  return fromCartesian([x, y, z]);
}

function toCartesian([lat, lng]: LatLng): [number, number, number] {
  const phi = (lat * Math.PI) / 180;
  const lam = (lng * Math.PI) / 180;
  const cp = Math.cos(phi);
  return [cp * Math.cos(lam), cp * Math.sin(lam), Math.sin(phi)];
}

function fromCartesian([x, y, z]: [number, number, number]): LatLng {
  const lat = (Math.atan2(z, Math.hypot(x, y)) * 180) / Math.PI;
  const lng = (Math.atan2(y, x) * 180) / Math.PI;
  return [lat, lng];
}

function haversineKm(a: LatLng, b: LatLng): number {
  const dLat = ((b[0] - a[0]) * Math.PI) / 180;
  const dLng = ((b[1] - a[1]) * Math.PI) / 180;
  const lat1 = (a[0] * Math.PI) / 180;
  const lat2 = (b[0] * Math.PI) / 180;
  const h = Math.sin(dLat / 2) ** 2 + Math.sin(dLng / 2) ** 2 * Math.cos(lat1) * Math.cos(lat2);
  return 2 * EARTH_KM * Math.asin(Math.sqrt(h));
}
