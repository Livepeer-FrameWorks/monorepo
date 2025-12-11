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

export function formatDate(dateString: string): string {
  return new Date(dateString).toLocaleString();
}

export function formatDuration(seconds: number): string {
  if (!seconds) return "N/A";
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const secs = seconds % 60;
  return `${hours.toString().padStart(2, "0")}:${minutes.toString().padStart(2, "0")}:${secs.toString().padStart(2, "0")}`;
}
