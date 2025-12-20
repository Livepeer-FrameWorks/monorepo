export function getStatusColor(status: string | undefined): string {
  switch (status?.toLowerCase()) {
    case "live":
    case "online":
    case "active":
      return "text-success";
    case "offline":
    case "inactive":
      return "text-error";
    case "recording":
      return "text-warning";
    default:
      return "text-muted-foreground";
  }
}

export function getStatusIcon(status: string | undefined): string {
  switch (status?.toLowerCase()) {
    case "live":
    case "online":
    case "active":
      return "Radio";
    case "offline":
    case "inactive":
      return "RadioOff";
    case "recording":
      return "Video";
    default:
      return "Circle";
  }
}

export function formatDate(dateString: string | null | undefined): string {
  if (!dateString) return "—";
  const date = new Date(dateString);
  return isNaN(date.getTime()) ? "—" : date.toLocaleString();
}

export function formatDuration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined) return "N/A";
  if (seconds === 0) return "00:00:00";
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const secs = Math.floor(seconds % 60);
  return `${hours.toString().padStart(2, "0")}:${minutes.toString().padStart(2, "0")}:${secs.toString().padStart(2, "0")}`;
}
