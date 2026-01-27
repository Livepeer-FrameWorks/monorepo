import { forwardRef } from "react";
import { cn } from "@/lib/utils";

const MarketingGridSeam = forwardRef(
  (
    {
      as: Component = "div",
      columns = 2,
      stackAt = "md",
      gap = "md",
      surface = "transparent",
      className,
      style,
      children,
      ...props
    },
    ref
  ) => (
    <Component
      ref={ref}
      className={cn(
        "marketing-grid-seam",
        columns && `marketing-grid-seam--cols-${columns}`,
        stackAt && `marketing-grid-seam--stack-${stackAt}`,
        gap && `marketing-grid-seam--gap-${gap}`,
        className
      )}
      data-surface={surface}
      style={{
        "--grid-cols": columns,
        ...style,
      }}
      {...props}
    >
      {children}
    </Component>
  )
);

MarketingGridSeam.displayName = "MarketingGridSeam";

export default MarketingGridSeam;
