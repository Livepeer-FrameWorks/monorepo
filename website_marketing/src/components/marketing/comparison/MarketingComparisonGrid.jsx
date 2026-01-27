import { forwardRef } from "react";
import { cn } from "@/lib/utils";
import MarketingGridSeam from "../layout/MarketingGridSeam";
import MarketingComparisonCard from "./MarketingComparisonCard";

const MarketingComparisonGrid = forwardRef(
  (
    {
      items = [],
      columns = 2,
      stackAt = "md",
      gap = "none",
      tone = "accent",
      className,
      renderCard,
      ...props
    },
    ref
  ) => (
    <MarketingGridSeam
      ref={ref}
      columns={columns}
      stackAt={stackAt}
      gap={gap}
      className={cn("marketing-comparison-grid", className)}
      data-columns={columns}
      {...props}
    >
      {items.map((item, index) => (
        <div key={item.id ?? index} className="marketing-comparison-grid__cell">
          {renderCard ? (
            renderCard(item, index)
          ) : (
            <MarketingComparisonCard {...item} tone={item.tone ?? tone} />
          )}
        </div>
      ))}
    </MarketingGridSeam>
  )
);

MarketingComparisonGrid.displayName = "MarketingComparisonGrid";

export default MarketingComparisonGrid;
