const DEFAULT_GAP_PX = 4;

/**
 * Offsets markers in screen space when their icons would visually overlap at the
 * current zoom. Members of an overlapping group are placed in a ring around the
 * group's pixel centroid; popups still report the true location stored on the marker.
 *
 * @param {import("leaflet").Map} map
 * @param {Array<{marker: import("leaflet").Marker, iconRadius: number}>} items
 * @param {{gapPx?: number}} [opts]
 */
export function spreadOverlappingMarkers(map, items, opts = {}) {
  if (items.length < 2) {
    items.forEach((it) => resetToOriginal(it.marker));
    return;
  }
  const gap = opts.gapPx ?? DEFAULT_GAP_PX;

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
      const threshold = items[i].iconRadius + items[j].iconRadius + gap;
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

    const ringRadius = maxRadius + gap + 2;
    const startAngle = -Math.PI / 2;

    indices.forEach((idx, i) => {
      const angle = startAngle + (2 * Math.PI * i) / indices.length;
      const px = cx + ringRadius * Math.cos(angle);
      const py = cy + ringRadius * Math.sin(angle);
      const newLatLng = map.containerPointToLatLng([px, py]);
      items[idx].marker.setLatLng(newLatLng);
    });
  });
}

function resetToOriginal(marker) {
  if (marker.__originalLatLng === undefined) {
    marker.__originalLatLng = marker.getLatLng();
    return;
  }
  marker.setLatLng(marker.__originalLatLng);
}
