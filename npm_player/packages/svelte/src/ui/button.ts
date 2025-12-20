export const buttonVariants = (variant: ButtonVariant = "default", size: ButtonSize = "default", className?: string) => {
  const baseClasses = "inline-flex items-center justify-center whitespace-nowrap rounded-md text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 disabled:pointer-events-none disabled:opacity-50 ring-offset-background";

  const variants = {
    default: "bg-primary text-primary-foreground hover:bg-primary/90",
    secondary: "bg-secondary text-secondary-foreground hover:bg-secondary/80",
    ghost: "hover:bg-accent hover:text-accent-foreground",
    outline: "border border-border bg-transparent hover:bg-accent hover:text-accent-foreground",
    destructive: "bg-destructive text-destructive-foreground hover:bg-destructive/90",
    subtle: "bg-muted text-muted-foreground hover:bg-muted/80",
    link: "text-primary underline-offset-4 hover:underline"
  };

  const sizes = {
    default: "h-10 px-4 py-2",
    sm: "h-9 rounded-md px-3",
    lg: "h-11 rounded-md px-8",
    icon: "h-9 w-9"
  };

  const selectedVariant = variants[variant] || variants.default;
  const selectedSize = sizes[size] || sizes.default;

  return `${baseClasses} ${selectedVariant} ${selectedSize} ${className || ""}`;
};

export type ButtonVariant = "default" | "secondary" | "ghost" | "outline" | "destructive" | "subtle" | "link";
export type ButtonSize = "default" | "sm" | "lg" | "icon";

// For type inference with svelte components, similar to VariantProps
export type ButtonProps = {
  variant?: ButtonVariant;
  size?: ButtonSize;
  className?: string;
};
