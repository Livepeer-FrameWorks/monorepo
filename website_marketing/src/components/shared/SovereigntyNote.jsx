import { InformationCircleIcon } from "@heroicons/react/24/outline";
import { Tooltip, TooltipTrigger, TooltipContent, TooltipProvider } from "@/components/ui/tooltip";
import config from "../../config";
import { cn } from "@/lib/utils";

const ROADMAP_URL = `${(config.docsUrl ?? "/docs").replace(/\/+$/, "")}/roadmap`;

// Small hover popup that links to the roadmap, dropped next to self-host /
// sovereignty mentions. Hosted and full self-host ship today; deeper BYOC
// levels are roadmap, so this keeps the claim honest without a heavy section.
const SovereigntyNote = ({
  children = "Run hosted, hybrid, or self-host the whole stack today. Deeper bring-your-own-cloud levels (private media clusters, dedicated hosted clusters, fully sovereign) are on the roadmap.",
  label = "How self-hosting and sovereignty work",
  className = "",
}) => (
  <TooltipProvider delayDuration={150}>
    <Tooltip>
      <TooltipTrigger asChild>
        <a
          href={ROADMAP_URL}
          target="_blank"
          rel="noopener noreferrer"
          aria-label={label}
          className={cn(
            "inline-flex h-5 w-5 items-center justify-center rounded-full border border-border/60 bg-transparent align-middle text-muted-foreground transition-colors hover:text-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40",
            className
          )}
        >
          <InformationCircleIcon className="h-3.5 w-3.5" />
        </a>
      </TooltipTrigger>
      <TooltipContent side="top" align="center" className="max-w-xs text-left leading-relaxed">
        <span className="block">{children}</span>
        <span className="mt-1 block font-medium text-accent">See the roadmap</span>
      </TooltipContent>
    </Tooltip>
  </TooltipProvider>
);

export default SovereigntyNote;
