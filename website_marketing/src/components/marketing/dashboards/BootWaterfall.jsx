import { useState } from "react";

// Time-to-first-frame waterfall: each boot span laid end-to-end on one timeline,
// so the total reads as "time to first frame". Hovering a segment or row lifts it.
export function BootWaterfall({ stages, cacheHitRatio }) {
  const [active, setActive] = useState(null);
  const total = stages.reduce((sum, s) => sum + s.ms, 0);

  return (
    <div className="boot-waterfall">
      <div className="boot-waterfall__head">
        <span className="boot-waterfall__metric">
          <span className="boot-waterfall__metric-value">{total}</span>
          <span className="boot-waterfall__metric-unit">ms to first frame</span>
        </span>
        {cacheHitRatio != null ? (
          <span className="boot-waterfall__cache">
            {Math.round(cacheHitRatio * 100)}% cache hit
          </span>
        ) : null}
      </div>

      <div
        className="boot-waterfall__track"
        role="img"
        aria-label={`Boot waterfall, ${total} milliseconds total`}
      >
        {stages.map((s, i) => (
          <span
            key={s.label}
            className="boot-waterfall__seg"
            data-active={active === i ? "" : undefined}
            style={{ width: `${(s.ms / total) * 100}%`, background: s.color }}
            onMouseEnter={() => setActive(i)}
            onMouseLeave={() => setActive(null)}
          />
        ))}
      </div>

      <ul className="boot-waterfall__legend">
        {stages.map((s, i) => (
          <li
            key={s.label}
            className="boot-waterfall__row"
            data-active={active === i ? "" : undefined}
            onMouseEnter={() => setActive(i)}
            onMouseLeave={() => setActive(null)}
          >
            <span
              className="boot-waterfall__chip"
              style={{ background: s.color }}
              aria-hidden="true"
            />
            <span className="boot-waterfall__row-label">{s.label}</span>
            <span className="boot-waterfall__row-hint">{s.hint}</span>
            <span className="boot-waterfall__row-ms">{s.ms} ms</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

export default BootWaterfall;
