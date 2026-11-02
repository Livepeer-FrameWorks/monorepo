import status from "@/data/status.json";
import { Badge } from "@/components/ui/badge";
import { featureOf, statusTone } from "@/lib/featureStatus";
import { cn } from "@/lib/utils";

// Inline status badge for one registry feature, labelled Shipping, Beta, or a
// tier tag from status.json.
const StatusTag = ({ feature, className = "" }) => {
  const entry = featureOf(status, feature);
  return (
    <span className={cn("inline-flex items-center", className)}>
      <Badge
        variant="outline"
        tone={statusTone(entry)}
        className="status-tag tracking-[0.18em] uppercase text-[0.65rem]"
        data-horizon={entry.horizon}
      >
        {entry.tag}
      </Badge>
    </span>
  );
};

export default StatusTag;
