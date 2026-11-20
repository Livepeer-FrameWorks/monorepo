/**
 * Describes a ladder's renditions for a hint: its sizes, and whether it has
 * high-frame-rate ("p60") renditions for 50 fps and faster sources.
 */
export function ladderSummary(renditionNames: readonly string[]): string {
  const sizes = renditionNames.filter((name) => !name.endsWith("p60"));
  const highRate = renditionNames.length > sizes.length;
  const list = sizes.join(", ");
  return highRate ? `${list}, with higher-bitrate renditions for 50 fps and faster sources` : list;
}
