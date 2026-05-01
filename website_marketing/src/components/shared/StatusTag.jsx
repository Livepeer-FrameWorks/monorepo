import InfoTooltip from "./InfoTooltip";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

const STATUS_COPY = {
  beta: "Beta: usable today with guidance; expect breaking changes and capacity limits.",
  pipeline: "Pipeline: in active development; not generally available yet.",
};

const StatusTag = ({ status = "beta", note, className = "" }) => {
  if (!status) return null;

  const label = status === "pipeline" ? "Pipeline" : "Beta";
  const tooltip = note || STATUS_COPY[status] || STATUS_COPY.beta;

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
