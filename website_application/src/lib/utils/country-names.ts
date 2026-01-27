import worldCountries from "world-countries";

// ISO2 → Full country name
export const countryNames: Record<string, string> = Object.fromEntries(
  worldCountries.map((c) => [c.cca2, c.name.common])
);

// ISO2 → ISO3 (for GeoJSON feature ID matching)
export const iso2ToIso3: Record<string, string> = Object.fromEntries(
  worldCountries.map((c) => [c.cca2, c.cca3])
);

// ISO3 → ISO2 (reverse lookup)
export const iso3ToIso2: Record<string, string> = Object.fromEntries(
  worldCountries.map((c) => [c.cca3, c.cca2])
);

/**
 * Get full country name from ISO 2-letter code
 * @param code - ISO 3166-1 alpha-2 code (e.g., "US", "NL")
 * @returns Full country name (e.g., "United States") or original code if not found
 */
export function getCountryName(code: string): string {
  if (!code) return code;
  const upper = code.toUpperCase();
  return countryNames[upper] || code;
}
