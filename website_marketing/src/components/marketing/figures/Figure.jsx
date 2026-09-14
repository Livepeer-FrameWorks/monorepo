import { cn } from "@/lib/utils";

/* Inline SVG diagram with an optional second drawing for narrow containers.
   The two drawings need different aspect ratios, so they are separate svg
   elements rather than groups sharing one viewBox; figures.css swaps them with
   a container query. */
const Figure = ({
  label,
  caption,
  viewBox,
  stackViewBox,
  stack,
  spot = false,
  className,
  children,
}) => {
  const hasStack = Boolean(stackViewBox && stack);

  return (
    <figure className={cn("fig-wrap", className)}>
      <svg
        className={cn("fig", spot && "fig--spot", hasStack && "on-wide")}
        viewBox={viewBox}
        role="img"
        aria-label={label}
      >
        {children}
      </svg>
      {hasStack ? (
        <svg
          className={cn("fig on-stack", spot && "fig--spot")}
          viewBox={stackViewBox}
          role="img"
          aria-label={label}
        >
          {stack}
        </svg>
      ) : null}
      {caption ? <figcaption>{caption}</figcaption> : null}
    </figure>
  );
};

export default Figure;
