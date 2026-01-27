import { forwardRef } from "react";
import { cn } from "@/lib/utils";

const MarketingOutline = forwardRef(
  (
    {
      as: Component = "div",
      label,
      labelPosition = "corner",
      accent = "accent",
      glow = false,
      contentClassName,
      className,
      children,
      ...props
    },
    ref
  ) => (
    <Component
      ref={ref}
      className={cn("marketing-outline", className)}
      data-accent={accent}
      data-label-position={labelPosition}
      data-glow={glow ? "true" : "false"}
      {...props}
    >
      {label ? (
        <span className="marketing-outline__label" aria-hidden="true">
          {label}
        </span>
      ) : null}
      <div className={cn("marketing-outline__content", contentClassName)}>{children}</div>
    </Component>
  )
);

MarketingOutline.displayName = "MarketingOutline";

export default MarketingOutline;
