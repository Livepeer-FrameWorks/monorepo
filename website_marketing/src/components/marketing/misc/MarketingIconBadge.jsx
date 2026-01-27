import { cn } from "@/lib/utils";

const MarketingIconBadge = ({ className, tone = "accent", variant = "gradient", children }) => (
  <span
    className={cn(
      "marketing-icon-badge",
      variant === "gradient" && tone ? `badge-tone-${tone}` : null,
      variant === "neutral" ? "marketing-icon-badge-neutral" : null,
      className
    )}
  >
    {children}
  </span>
);

export default MarketingIconBadge;
