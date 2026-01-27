import { forwardRef } from "react";
import { Slot } from "@radix-ui/react-slot";
import { cva } from "class-variance-authority";
import { cn } from "@/lib/utils";

const buttonVariants = cva(
  "inline-flex items-center justify-center gap-2 whitespace-nowrap rounded-lg text-sm font-semibold uppercase tracking-[0.08em] cursor-pointer transition-all duration-200 ease-out focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-background disabled:pointer-events-none disabled:opacity-50",
  {
    variants: {
      variant: {
        default:
          "bg-primary text-primary-foreground shadow-lg hover:bg-[hsl(221,89%,66%)] hover:-translate-y-0.5 hover:shadow-brand",
        secondary:
          "border border-primary/60 text-accent hover:border-accent hover:bg-accent/18 hover:text-accent hover:-translate-y-0.5 hover:shadow-brand-subtle",
        ghost: "bg-transparent text-foreground hover:bg-muted/40",
        link: "text-accent underline-offset-4 hover:underline",
      },
      size: {
        sm: "h-9 px-3 text-xs",
        md: "h-11 px-6",
        lg: "h-12 px-8 text-base",
        icon: "h-10 w-10",
      },
    },
    defaultVariants: {
      variant: "default",
      size: "md",
    },
  }
);

const Button = forwardRef(({ className, variant, size, asChild = false, ...props }, ref) => {
  const Comp = asChild ? Slot : "button";

  return <Comp ref={ref} className={cn(buttonVariants({ variant, size, className }))} {...props} />;
});

Button.displayName = "Button";

export { Button, buttonVariants };
