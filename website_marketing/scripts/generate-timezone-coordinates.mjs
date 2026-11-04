#!/usr/bin/env node
// Writes src/data/timezone-coordinates.json from an IANA tz `zone.tab`: each
// zone's reference point as [latitude, longitude], rounded to 0.1°. The map
// frames the network around the visitor's browser timezone with it.
//
//   node scripts/generate-timezone-coordinates.mjs [path/to/zone.tab]
//
// zone.tab ships with tzdata (public domain); the default is the system copy.
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
const source = process.argv[2] ?? "/usr/share/zoneinfo/zone.tab";
const target = resolve(here, "../src/data/timezone-coordinates.json");

// ISO 6709 sign-degrees-minutes[-seconds]: +DDMM, +DDMMSS, +DDDMM, +DDDMMSS.
function iso6709(part, degreeDigits) {
  const sign = part[0] === "-" ? -1 : 1;
  const digits = part.slice(1);
  const degrees = Number(digits.slice(0, degreeDigits));
  const minutes = Number(digits.slice(degreeDigits, degreeDigits + 2));
  const seconds = Number(digits.slice(degreeDigits + 2) || 0);
  return sign * (degrees + minutes / 60 + seconds / 3600);
}

const coordinates = {};
for (const line of readFileSync(source, "utf8").split("\n")) {
  if (!line || line.startsWith("#")) continue;
  const [, coord, zone] = line.split("\t");
  const match = /^([+-]\d+)([+-]\d+)$/.exec(coord ?? "");
  if (!zone || !match) continue;
  const lat = iso6709(match[1], 2);
  const lon = iso6709(match[2], 3);
  coordinates[zone] = [Math.round(lat * 10) / 10, Math.round(lon * 10) / 10];
}

const sorted = Object.fromEntries(
  Object.entries(coordinates).sort(([a], [b]) => a.localeCompare(b))
);
writeFileSync(target, `${JSON.stringify(sorted, null, 0)}\n`);
console.log(`wrote ${Object.keys(sorted).length} zones to ${target}`);
