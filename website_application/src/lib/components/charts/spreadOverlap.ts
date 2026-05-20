import type { CircleMarker, Map as LMap, Marker } from "leaflet";

export interface Spreadable {
  marker: Marker | CircleMarker;
  iconRadius: number;
}

interface MarkerWithOriginal {
  getLatLng: Marker["getLatLng"];
  setLatLng: Marker["setLatLng"];
  __originalLatLng?: ReturnType<Marker["getLatLng"]>;
}

const DEFAULT_GAP_PX = 4;

/**
 * Offsets markers in screen space when their icons would visually overlap at the
 * current zoom. Members of an overlapping group are placed in a compact hex/pin
 * pack around the group's pixel centroid; detail panels read from source data,
 * not marker coords.
 */
export function spreadOverlappingMarkers(
  map: LMap,
  items: Spreadable[],
  opts: { gapPx?: number } = {}
): void {
  if (items.length < 2) {
    items.forEach((it) => resetToOriginal(it.marker));
    return;
  }
  const gap = opts.gapPx ?? DEFAULT_GAP_PX;

  items.forEach((it) => resetToOriginal(it.marker));

  const points = items.map((it) => {
    const ll = it.marker.getLatLng();
    return map.latLngToContainerPoint(ll);
  });

  const parent = items.map((_, i) => i);
  const find = (i: number): number => {
    while (parent[i] !== i) {
      parent[i] = parent[parent[i]];
      i = parent[i];
    }
    return i;
  };
  const union = (a: number, b: number) => {
    const ra = find(a);
    const rb = find(b);
    if (ra !== rb) parent[ra] = rb;
  };

  for (let i = 0; i < items.length; i++) {
    for (let j = i + 1; j < items.length; j++) {
      const dx = points[i].x - points[j].x;
      const dy = points[i].y - points[j].y;
      const dist = Math.sqrt(dx * dx + dy * dy);
      const threshold = items[i].iconRadius + items[j].iconRadius + gap;
      if (dist < threshold) union(i, j);
    }
  }

  const groups = new Map<number, number[]>();
  for (let i = 0; i < items.length; i++) {
    const root = find(i);
    const list = groups.get(root);
    if (list) list.push(i);
    else groups.set(root, [i]);
  }

  groups.forEach((indices) => {
    if (indices.length < 2) return;

    let cx = 0;
    let cy = 0;
    let maxRadius = 0;
    indices.forEach((idx) => {
      cx += points[idx].x;
      cy += points[idx].y;
      if (items[idx].iconRadius > maxRadius) maxRadius = items[idx].iconRadius;
    });
    cx /= indices.length;
    cy /= indices.length;

    const step = 2 * maxRadius + gap;
    const offsets = groupOffsets(indices.length, step);

    indices.forEach((idx, i) => {
      const [ox, oy] = offsets[i];
      const newLatLng = map.containerPointToLatLng([cx + ox, cy + oy]);
      items[idx].marker.setLatLng(newLatLng);
    });
  });
}

/**
 * Hex/pin packed offsets around (0,0) so the centroid stays put. Small groups
 * get hand-tuned layouts (pair, billiards-rack tri); larger groups fall back to
 * a pointy-top hex spiral with the centroid recentered.
 */
function groupOffsets(n: number, step: number): Array<[number, number]> {
  if (n === 2) {
    return [
      [-step / 2, 0],
      [step / 2, 0],
    ];
  }
  if (n === 3) {
    const R = step / Math.sqrt(3);
    return [
      [0, -R],
      [-step / 2, R / 2],
      [step / 2, R / 2],
    ];
  }
  const axial = hexSpiral(n);
  const pts = axial.map(
    ([q, r]) => [step * (q + r / 2), step * (Math.sqrt(3) / 2) * r] as [number, number]
  );
  let cx = 0;
  let cy = 0;
  pts.forEach(([x, y]) => {
    cx += x;
    cy += y;
  });
  cx /= n;
  cy /= n;
  return pts.map(([x, y]) => [x - cx, y - cy] as [number, number]);
}

function hexSpiral(n: number): Array<[number, number]> {
  const out: Array<[number, number]> = [[0, 0]];
  if (n <= 1) return out;
  const dirs: Array<[number, number]> = [
    [1, 0],
    [1, -1],
    [0, -1],
    [-1, 0],
    [-1, 1],
    [0, 1],
  ];
  let k = 1;
  while (out.length < n) {
    let q = k * dirs[0][0];
    let r = k * dirs[0][1];
    for (let side = 0; side < 6 && out.length < n; side++) {
      const [dq, dr] = dirs[(side + 2) % 6];
      for (let i = 0; i < k && out.length < n; i++) {
        out.push([q, r]);
        q += dq;
        r += dr;
      }
    }
    k++;
  }
  return out;
}

function resetToOriginal(marker: Marker | CircleMarker): void {
  const m = marker as MarkerWithOriginal;
  if (m.__originalLatLng === undefined) {
    m.__originalLatLng = marker.getLatLng();
    return;
  }
  marker.setLatLng(m.__originalLatLng);
}
