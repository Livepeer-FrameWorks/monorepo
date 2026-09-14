/* Rendered once per page by the root layout. Every `.fig .edge` marker points here. */
const heads = [
  ["fig-arrow", "head-accent"],
  ["fig-arrow-rev", "head-accent"],
  ["fig-arrow-warm", "head-warm"],
  ["fig-arrow-dim", "head-dim"],
  ["fig-arrow-bad", "head-bad"],
];

const FigureDefs = () => (
  <svg
    className="fig-defs"
    width="0"
    height="0"
    aria-hidden="true"
    style={{ position: "absolute" }}
  >
    <defs>
      {heads.map(([id, headClass]) => (
        <marker
          key={id}
          id={id}
          viewBox="0 0 10 10"
          refX="9"
          refY="5"
          markerWidth="6"
          markerHeight="6"
          orient="auto-start-reverse"
        >
          <path className={headClass} d="M0 0 L10 5 L0 10 z" />
        </marker>
      ))}
    </defs>
  </svg>
);

export default FigureDefs;
