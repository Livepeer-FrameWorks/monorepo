import { Children, forwardRef } from "react";
import { cn } from "@/lib/utils";
import { renderSlot } from "../utils";

const MarketingStackedSeam = forwardRef(
  ({ items = [], children, gap = "md", align = "start", className, renderItem, ...props }, ref) => {
    const content = items.length
      ? items.map((item, index) => (renderItem ? renderItem(item, index) : renderSlot(item)))
      : Children.toArray(children);

    return (
      <div
        ref={ref}
        className={cn(
          "marketing-stacked-seam",
          gap && gap !== "md" && `marketing-stacked-seam--gap-${gap}`,
          align && `marketing-stacked-seam--align-${align}`,
          className
        )}
        {...props}
      >
        {content.map((child, index) => (
          <div key={index} className="marketing-stacked-seam__item">
            {renderSlot(child)}
          </div>
        ))}
      </div>
    );
  }
);

MarketingStackedSeam.displayName = "MarketingStackedSeam";

export default MarketingStackedSeam;
