import { forwardRef } from "react";
import { cn } from "@/lib/utils";

const MarketingBand = forwardRef(
  (
    {
      as: Component = "section",
      surface = "none",
      bleed = false,
      contentClassName,
      className,
      children,
      ...props
    },
    ref
  ) => {
    return (
      <Component
        ref={ref}
        className={cn("marketing-band", bleed && "marketing-band--bleed", className)}
        data-surface={surface}
        {...props}
      >
        <div className={cn("marketing-band__inner", contentClassName)}>{children}</div>
      </Component>
    );
  }
);

MarketingBand.displayName = "MarketingBand";

export default MarketingBand;
