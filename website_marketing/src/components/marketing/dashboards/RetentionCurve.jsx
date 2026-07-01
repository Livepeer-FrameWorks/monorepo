import { useMemo, useRef, useState } from "react";

// Audience-retention curve plus a "most replayed" density strip: a fresh take on
// the webapp's VodRetentionChart (the winning interaction here is a candidate to
// fold back into the product). Pure SVG, smoothed curve, gradient fill, hover scrub.

const W = 1000;
const PLOT_TOP = 14;
const PLOT_BOTTOM = 150;
const STRIP_TOP = 166;
const STRIP_H = 28;
const H = STRIP_TOP + STRIP_H + 2;

function tlabel(sec) {
  const s = Math.round(sec);
  const m = Math.floor(s / 60);
  const r = s % 60;
  return m > 0 ? `${m}:${String(r).padStart(2, "0")}` : `${r}s`;
}

// Catmull-Rom to cubic bezier so the curve reads smooth, not segmented.
function smoothLine(pts) {
  if (pts.length < 2) return "";
  let d = `M ${pts[0][0].toFixed(1)} ${pts[0][1].toFixed(1)}`;
  for (let i = 0; i < pts.length - 1; i++) {
    const p0 = pts[i - 1] || pts[i];
    const p1 = pts[i];
    const p2 = pts[i + 1];
    const p3 = pts[i + 2] || p2;
    const c1x = p1[0] + (p2[0] - p0[0]) / 6;
    const c1y = p1[1] + (p2[1] - p0[1]) / 6;
    const c2x = p2[0] - (p3[0] - p1[0]) / 6;
    const c2y = p2[1] - (p3[1] - p1[1]) / 6;
    d += ` C ${c1x.toFixed(1)} ${c1y.toFixed(1)}, ${c2x.toFixed(1)} ${c2y.toFixed(1)}, ${p2[0].toFixed(1)} ${p2[1].toFixed(1)}`;
  }
  return d;
}

export function RetentionCurve({ points, totalSessions, bucketWidthS, assetDurationS }) {
  const wrapRef = useRef(null);
  const [hover, setHover] = useState(null);

  const model = useMemo(() => {
    const bucketCount =
      bucketWidthS > 0 && assetDurationS > 0
        ? Math.ceil(assetDurationS / bucketWidthS)
        : points.length;
    const byIndex = new Map(points.map((p) => [p.bucketIndex, p]));
    const n = Math.max(bucketCount, 1);
    const dense = [];
    for (let i = 0; i < n; i++) {
      const p = byIndex.get(i);
      dense.push({
        retention: p && totalSessions > 0 ? Math.min(1, p.reached / totalSessions) : 0,
        density: p ? p.secondsWatched : 0,
      });
    }
    const maxDensity = Math.max(1, ...dense.map((d) => d.density));
    const dx = W / n;
    const linePts = dense.map((d, i) => [
      i * dx + dx / 2,
      PLOT_BOTTOM - d.retention * (PLOT_BOTTOM - PLOT_TOP),
    ]);
    const line = smoothLine(linePts);
    const area = line ? `${line} L ${W} ${PLOT_BOTTOM} L 0 ${PLOT_BOTTOM} Z` : "";
    let peakIdx = 0;
    dense.forEach((d, i) => {
      if (d.density > dense[peakIdx].density) peakIdx = i;
    });
    return { dense, maxDensity, dx, linePts, line, area, peakIdx, n };
  }, [points, totalSessions, bucketWidthS, assetDurationS]);

  if (model.dense.length === 0 || totalSessions === 0) {
    return <div className="retention__empty">No retention data for this asset yet.</div>;
  }

  const onMove = (e) => {
    const rect = wrapRef.current?.getBoundingClientRect();
    if (!rect) return;
    const frac = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width));
    setHover(Math.min(model.n - 1, Math.round(frac * (model.n - 1))));
  };

  const hoverPt = hover != null ? model.linePts[hover] : null;
  const hoverData = hover != null ? model.dense[hover] : null;
  const peakX = model.linePts[model.peakIdx][0];
  const peakFrac = peakX / W;
  const peakEdge = peakFrac > 0.78 ? "right" : peakFrac < 0.22 ? "left" : "mid";

  return (
    <div className="retention">
      <div
        ref={wrapRef}
        className="retention__plot"
        onMouseMove={onMove}
        onMouseLeave={() => setHover(null)}
        role="img"
        aria-label="Audience retention curve with most-replayed density"
      >
        <svg viewBox={`0 0 ${W} ${H}`} className="retention__svg" preserveAspectRatio="none">
          <defs>
            <linearGradient id="retention-fill" x1="0" y1="0" x2="0" y2="1">
              <stop offset="0%" stopColor="rgb(125, 207, 255)" stopOpacity="0.34" />
              <stop offset="100%" stopColor="rgb(125, 207, 255)" stopOpacity="0.02" />
            </linearGradient>
          </defs>

          {/* gridlines at 25/50/75% */}
          {[0.25, 0.5, 0.75].map((g) => {
            const y = PLOT_BOTTOM - g * (PLOT_BOTTOM - PLOT_TOP);
            return <line key={g} x1="0" y1={y} x2={W} y2={y} className="retention__grid" />;
          })}

          <path d={model.area} fill="url(#retention-fill)" />
          <path d={model.line} className="retention__line" fill="none" />

          {/* density strip ("most replayed") */}
          {model.dense.map((d, i) => (
            <rect
              key={i}
              x={i * model.dx}
              y={STRIP_TOP}
              width={Math.max(model.dx - 0.6, 0.6)}
              height={STRIP_H}
              fill="rgb(255, 158, 100)"
              opacity={0.1 + 0.9 * (d.density / model.maxDensity)}
            />
          ))}

          {/* most-replayed marker */}
          <line
            x1={peakX}
            y1={PLOT_TOP}
            x2={peakX}
            y2={STRIP_TOP + STRIP_H}
            className="retention__peak"
          />

          {hoverPt ? (
            <>
              <line
                x1={hoverPt[0]}
                y1={PLOT_TOP}
                x2={hoverPt[0]}
                y2={STRIP_TOP + STRIP_H}
                className="retention__cursor"
              />
              <circle cx={hoverPt[0]} cy={hoverPt[1]} r="5" className="retention__dot" />
            </>
          ) : null}
        </svg>

        <span
          className="retention__peak-pill"
          data-edge={peakEdge}
          style={{ left: `${peakFrac * 100}%` }}
        >
          Most replayed
        </span>

        {hoverData ? (
          <div
            className="retention__tip"
            style={{ left: `${(hoverPt[0] / W) * 100}%` }}
            data-edge={hoverPt[0] / W > 0.7 ? "right" : hoverPt[0] / W < 0.3 ? "left" : "mid"}
          >
            <span className="retention__tip-time">{tlabel(hover * bucketWidthS)}</span>
            <span className="retention__tip-row">
              <span className="retention__tip-dot retention__tip-dot--curve" />
              {Math.round(hoverData.retention * 100)}% still watching
            </span>
            <span className="retention__tip-row">
              <span className="retention__tip-dot retention__tip-dot--density" />
              {Math.round((hoverData.density / model.maxDensity) * 100)}% of peak replay
            </span>
          </div>
        ) : null}
      </div>

      <div className="retention__axis">
        <span>0:00</span>
        <span>{tlabel(assetDurationS / 2)}</span>
        <span>{tlabel(assetDurationS)}</span>
      </div>

      <div className="retention__legend">
        <span>
          <i className="retention__swatch retention__swatch--curve" /> audience retention
        </span>
        <span>
          <i className="retention__swatch retention__swatch--density" /> most replayed (watch
          density)
        </span>
      </div>
    </div>
  );
}

export default RetentionCurve;
