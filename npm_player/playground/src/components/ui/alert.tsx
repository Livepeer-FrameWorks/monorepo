import * as React from "react";
import { AlertTriangle, Info } from "lucide-react";
import { cn } from "@/lib/utils";

const icons = {
  info: Info,
  warning: AlertTriangle
};

export type AlertVariant = keyof typeof icons;

export interface AlertProps extends React.HTMLAttributes<HTMLDivElement> {
  variant?: AlertVariant;
}

const Alert = React.forwardRef<HTMLDivElement, AlertProps>(({ className, variant = "info", children, ...props }, ref) => {
  const Icon = icons[variant];
  return (
    <div
      ref={ref}
      role="alert"
      className={cn(
        "flex w-full items-start gap-3 rounded-lg border border-border bg-muted/50 px-4 py-3 text-sm text-muted-foreground",
        className
      )}
      {...props}
    >
      <Icon className="mt-0.5 h-4 w-4 text-primary" aria-hidden="true" />
      <div>{children}</div>
    </div>
  );
});
Alert.displayName = "Alert";

export { Alert };
