const DEFAULT_GAP_PX = 4;

/**
 * Offsets markers in screen space when their icons would visually overlap at the
 * current zoom. Members of an overlapping group are placed in a compact hex/pin
 * pack around the group's pixel centroid; detail panels read from source data,
 * not marker coords.
 *
 * @param {import("leaflet").Map} map
 * @param {Array<{marker: import("leaflet").Marker, iconRadius: number}>} items
 * @param {{gapPx?: number, groupThresholdMultiplier?: number, maxExpandedGroupSize?: number, denseStepScale?: number}} [opts]
 */
export function spreadOverlappingMarkers(map, items, opts = {}) {
  if (items.length < 2) {
    items.forEach((it) => resetToOriginal(it.marker));
    return;
  }
  const gap = opts.gapPx ?? DEFAULT_GAP_PX;
  const thresholdMultiplier = opts.groupThresholdMultiplier ?? 1;
  const maxExpandedGroupSize = opts.maxExpandedGroupSize ?? Number.POSITIVE_INFINITY;
  const denseStepScale = opts.denseStepScale ?? 1;

  items.forEach((it) => resetToOriginal(it.marker));

  const points = items.map((it) => map.latLngToContainerPoint(it.marker.getLatLng()));

  const parent = items.map((_, i) => i);
  const find = (i) => {
    while (parent[i] !== i) {
      parent[i] = parent[parent[i]];
      i = parent[i];
    }
    return i;
  };
  const union = (a, b) => {
    const ra = find(a);
    const rb = find(b);
    if (ra !== rb) parent[ra] = rb;
  };

  for (let i = 0; i < items.length; i++) {
    for (let j = i + 1; j < items.length; j++) {
      const dx = points[i].x - points[j].x;
      const dy = points[i].y - points[j].y;
      const dist = Math.sqrt(dx * dx + dy * dy);
      const threshold = (items[i].iconRadius + items[j].iconRadius + gap) * thresholdMultiplier;
      if (dist < threshold) union(i, j);
    }
  }

  const groups = new Map();
  for (let i = 0; i < items.length; i++) {
    const root = find(i);
    const list = groups.get(root);
    if (list) list.push(i);
    else groups.set(root, [i]);
  }

  groups.forEach((indices) => {
    if (indices.length < 2) return;

    // Anchor at the largest item so big clusters don't drift onto a node and
    // smaller items fan around them. Tie-break by original index for stability.
    indices.sort((a, b) => items[b].iconRadius - items[a].iconRadius || a - b);
    const anchor = points[indices[0]];
    const rMax = items[indices[0]].iconRadius;
    const rSecond = items[indices[1]].iconRadius;
    // Step = clearance between the two largest + gap. For uniform groups this
    // collapses to 2r+gap; for mixed groups (cluster + nodes) it stays tight
    // instead of inflating to 2 * cluster radius and leaving dead space.
    const step =
      (rMax + rSecond + gap) * (indices.length > maxExpandedGroupSize ? denseStepScale : 1);
    const offsets = groupOffsets(indices.length, step, maxExpandedGroupSize);

    indices.forEach((idx, i) => {
      const [ox, oy] = offsets[i];
      const newLatLng = map.containerPointToLatLng([anchor.x + ox, anchor.y + oy]);
      items[idx].marker.setLatLng(newLatLng);
    });
  });
}

/**
 * Hex/pin packed offsets with item 0 fixed at (0,0). The caller anchors that
 * slot at the largest item's real position so it stays put; smaller items fan
 * out around it. Small groups get hand-tuned layouts; larger groups use a
 * pointy-top hex spiral whose first cell is the anchor.
 *
 * @param {number} n
 * @param {number} step
 * @returns {Array<[number, number]>}
 */
function groupOffsets(n, step, maxExpandedGroupSize = Number.POSITIVE_INFINITY) {
  const slots = Math.max(1, Math.min(n, maxExpandedGroupSize));
  // Item 0 always sits at (0,0). The caller anchors that slot at the largest
  // item's real pixel position, so it doesn't move.
  if (slots === 2) {
    const base = [
      [0, 0],
      [step, 0],
    ];
    return repeatSlots(n, base);
  }
  if (slots === 3) {
    const h = (step * Math.sqrt(3)) / 2;
    const base = [
      [0, 0],
      [-step / 2, h],
      [step / 2, h],
    ];
    return repeatSlots(n, base);
  }
  const base = hexSpiral(slots).map(([q, r]) => [
    step * (q + r / 2),
    step * (Math.sqrt(3) / 2) * r,
  ]);
  return repeatSlots(n, base);
}

function repeatSlots(n, slots) {
  return Array.from({ length: n }, (_, i) => slots[i % slots.length]);
}

function hexSpiral(n) {
  const out = [[0, 0]];
  if (n <= 1) return out;
  const dirs = [
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

function resetToOriginal(marker) {
  if (marker.__originalLatLng === undefined) {
    marker.__originalLatLng = marker.getLatLng();
    return;
  }
  marker.setLatLng(marker.__originalLatLng);
}
