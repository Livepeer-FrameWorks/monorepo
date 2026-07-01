import { cn } from "@/lib/utils";

// Tiny inline sparkline (chrome only). Maps a 0..1-ish series to a polyline in a
// fixed viewBox; CSS scales it to the card width.
function Sparkline({ values, color = "rgb(125, 207, 255)" }) {
  if (!values?.length) return null;
  const W = 100;
  const H = 26;
  const min = Math.min(...values);
  const max = Math.max(...values);
  const span = max - min || 1;
  const step = W / (values.length - 1);
  const pts = values.map(
    (v, i) => `${(i * step).toFixed(1)},${(H - ((v - min) / span) * (H - 4) - 2).toFixed(1)}`
  );
  return (
    <svg
      className="stat-card__spark"
      viewBox={`0 0 ${W} ${H}`}
      preserveAspectRatio="none"
      aria-hidden="true"
    >
      <polyline
        points={pts.join(" ")}
        fill="none"
        stroke={color}
        strokeWidth="1.5"
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}

// 4-up metric cards. Responsive 4 to 2x2 to 1 per the design system; seams, not gaps.
export function StatRow({ stats }) {
  return (
    <div className="stat-row">
      {stats.map((s) => (
        <div key={s.label} className="stat-card">
          <span className="stat-card__label">{s.label}</span>
          <span className="stat-card__value">{s.value}</span>
          <span className="stat-card__foot">
            {s.delta ? (
              <span
                className={cn("stat-card__delta", s.trend === "down" && "stat-card__delta--down")}
              >
                {s.delta}
              </span>
            ) : null}
            {s.sub ? <span className="stat-card__sub">{s.sub}</span> : null}
          </span>
          {s.spark ? <Sparkline values={s.spark} /> : null}
        </div>
      ))}
    </div>
  );
}

export default StatRow;
