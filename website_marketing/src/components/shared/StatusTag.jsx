import InfoTooltip from "./InfoTooltip";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

const STATUS_COPY = {
  alpha: "Alpha: usable today with guidance; expect breaking changes and capacity limits.",
  pipeline: "Pipeline: in active development; not generally available yet.",
};

const StatusTag = ({ status = "alpha", note, className = "" }) => {
  if (!status) return null;

  const label = status === "pipeline" ? "Pipeline" : "Alpha";
  const tooltip = note || STATUS_COPY[status] || STATUS_COPY.alpha;

  return (
    <span className={cn("inline-flex items-center gap-1", className)}>
      <Badge variant="outline" className="tracking-[0.18em] uppercase text-[0.65rem]">
        {label}
      </Badge>
      <InfoTooltip position="top">{tooltip}</InfoTooltip>
    </span>
  );
};

export default StatusTag;
