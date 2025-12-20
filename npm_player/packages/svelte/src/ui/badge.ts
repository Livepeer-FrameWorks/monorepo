export const badgeVariants = (variant: BadgeVariant = "default", className?: string) => {
  const baseClasses = "inline-flex items-center rounded-full border border-transparent px-2.5 py-0.5 text-xs font-semibold transition-colors focus:outline-none focus:ring-2 focus:ring-ring focus:ring-offset-2 ring-offset-background";

  const variants = {
    default: "bg-primary text-primary-foreground hover:bg-primary/80",
    secondary: "bg-secondary text-secondary-foreground hover:bg-secondary/80",
    outline: "border-border text-foreground"
  };

  const selectedVariant = variants[variant] || variants.default;

  return `${baseClasses} ${selectedVariant} ${className || ""}`;
};

export type BadgeVariant = "default" | "secondary" | "outline";

export type BadgeProps = {
  variant?: BadgeVariant;
  className?: string;
};
