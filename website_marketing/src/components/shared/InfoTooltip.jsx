import { InformationCircleIcon } from "@heroicons/react/24/outline";
import { Tooltip, TooltipTrigger, TooltipContent, TooltipProvider } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

const InfoTooltip = ({ children, label = "More info", position = "top", className = "" }) => {
  const side = ["top", "right", "bottom", "left"].includes(position) ? position : "top";

  return (
    <TooltipProvider delayDuration={150}>
      <Tooltip>
        <TooltipTrigger asChild>
          <button
            type="button"
            aria-label={label}
            className={cn(
              "inline-flex h-5 w-5 items-center justify-center rounded-full border border-border/60 bg-transparent text-muted-foreground transition-colors hover:text-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40 focus-visible:ring-offset-2 focus-visible:ring-offset-background",
              className
            )}
          >
            <InformationCircleIcon className="h-3.5 w-3.5" />
          </button>
        </TooltipTrigger>
        <TooltipContent side={side} align="center" className="max-w-xs text-left leading-relaxed">
          {children}
        </TooltipContent>
      </Tooltip>
    </TooltipProvider>
  );
};

export default InfoTooltip;
