export function canPlayRollingDvr(status: string, hasMedia: boolean): boolean {
  // Completed parent recordings retain catalog metadata, not a playable file.
  // Their archived chapters are separate VOD artifacts.
  return (
    ["requested", "starting", "started", "recording"].includes(status.toLowerCase()) && hasMedia
  );
}
