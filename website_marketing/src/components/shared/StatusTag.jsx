import status from "@/data/status.json";
import { Badge } from "@/components/ui/badge";
import { featureOf, statusTone } from "@/lib/featureStatus";
import { cn } from "@/lib/utils";

// Inline status badge for one registry feature, labelled Shipping, Beta, or a
// tier tag from status.json. `withNote` prints the registry note under it, so
// the caveat is readable without a hover.
const StatusTag = ({ feature, withNote = false, className = "" }) => {
  const entry = featureOf(status, feature);
  const badge = (
    <Badge
      variant="outline"
      tone={statusTone(entry)}
      className="status-tag tracking-[0.18em] uppercase text-[0.65rem]"
      data-horizon={entry.horizon}
    >
      {entry.tag}
    </Badge>
  );

  if (!withNote || !entry.note) {
    return <span className={cn("inline-flex items-center", className)}>{badge}</span>;
  }

  return (
    <span className={cn("status-tag-block", className)}>
      {badge}
      <span className="status-tag-block__note">{entry.note}</span>
    </span>
  );
};

export default StatusTag;
