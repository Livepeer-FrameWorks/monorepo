import { Children, forwardRef } from "react";
import { cn } from "@/lib/utils";
import { renderSlot } from "../utils";

const MarketingGridSplit = forwardRef(
  (
    {
      primary,
      secondary,
      children,
      align = "center",
      gap = "lg",
      stackAt = "lg",
      reverse = false,
      bleed = false,
      seam = false,
      className,
      // eslint-disable-next-line no-unused-vars
      style,
      ...props
    },
    ref
  ) => {
    const slots = Children.toArray(children);
    const resolvedPrimary = primary ?? slots[0];
    const resolvedSecondary = secondary ?? slots[1];

    return (
      <div
        ref={ref}
        className={cn(
          "marketing-grid-split",
          align && `marketing-grid-split--align-${align}`,
          !seam && gap && `marketing-grid-split--gap-${gap}`,
          stackAt && `marketing-grid-split--stack-${stackAt}`,
          reverse && "marketing-grid-split--reverse",
          bleed && "marketing-grid-split--bleed",
          seam && "marketing-grid-split--seam",
          className
        )}
        {...props}
      >
        <div className="marketing-grid-split__primary">{renderSlot(resolvedPrimary)}</div>
        <div className="marketing-grid-split__secondary">{renderSlot(resolvedSecondary)}</div>
      </div>
    );
  }
);

MarketingGridSplit.displayName = "MarketingGridSplit";

export default MarketingGridSplit;
