/**
 * The choices the Encoder settings offer in every binding, and how a choice
 * becomes an encoder override. A choice equal to the quality profile's own
 * value clears the override, so the profile keeps deciding it.
 */

import type { EncoderOverrides, QualityProfile, VideoCodecPreference } from "../types";
import { getEncoderSettings } from "./MediaConstraints";

export interface EncoderOption<T> {
  value: T;
  label: string;
}

export const ENCODER_RESOLUTION_OPTIONS: EncoderOption<string>[] = [
  { value: "3840x2160", label: "3840×2160 (4K)" },
  { value: "2560x1440", label: "2560×1440 (1440p)" },
  { value: "1920x1080", label: "1920×1080 (1080p)" },
  { value: "1280x720", label: "1280×720 (720p)" },
  { value: "854x480", label: "854×480 (480p)" },
  { value: "640x360", label: "640×360 (360p)" },
];

export const ENCODER_VIDEO_BITRATE_OPTIONS: EncoderOption<number>[] = [
  { value: 50_000_000, label: "50 Mbps" },
  { value: 35_000_000, label: "35 Mbps" },
  { value: 25_000_000, label: "25 Mbps" },
  { value: 15_000_000, label: "15 Mbps" },
  { value: 10_000_000, label: "10 Mbps" },
  { value: 8_000_000, label: "8 Mbps" },
  { value: 6_000_000, label: "6 Mbps" },
  { value: 4_000_000, label: "4 Mbps" },
  { value: 2_500_000, label: "2.5 Mbps" },
  { value: 2_000_000, label: "2 Mbps" },
  { value: 1_500_000, label: "1.5 Mbps" },
  { value: 1_000_000, label: "1 Mbps" },
  { value: 500_000, label: "500 kbps" },
];

export const ENCODER_FRAMERATE_OPTIONS: EncoderOption<number>[] = [
  { value: 120, label: "120 fps" },
  { value: 60, label: "60 fps" },
  { value: 30, label: "30 fps" },
  { value: 24, label: "24 fps" },
  { value: 15, label: "15 fps" },
];

export const ENCODER_AUDIO_BITRATE_OPTIONS: EncoderOption<number>[] = [
  { value: 320_000, label: "320 kbps" },
  { value: 256_000, label: "256 kbps" },
  { value: 192_000, label: "192 kbps" },
  { value: 128_000, label: "128 kbps" },
  { value: 96_000, label: "96 kbps" },
  { value: 64_000, label: "64 kbps" },
];

export const ENCODER_CODEC_LABELS: Record<Exclude<VideoCodecPreference, "auto">, string> = {
  av1: "AV1",
  vp9: "VP9",
  h264: "H.264",
};

export type EncoderSetting = "resolution" | "framerate" | "videoBitrate" | "audioBitrate" | "codec";

/** One select of the Encoder section, ready for any binding to draw. */
export interface EncoderSettingRow {
  setting: EncoderSetting;
  labelKey: "videoCodec" | "resolution" | "framerate" | "videoBitrate" | "audioBitrate";
  value: string;
  options: EncoderOption<string>[];
  overridden: boolean;
}

function formatRate(bps: number): string {
  return bps >= 1_000_000 ? `${bps / 1_000_000} Mbps` : `${Math.round(bps / 1000)} kbps`;
}

function choices<T extends string | number>(
  options: EncoderOption<T>[],
  value: T,
  label: (value: T) => string
): EncoderOption<string>[] {
  const listed = options.map((o) => ({ value: String(o.value), label: o.label }));
  // A value set through the API may be off the list; the select still shows it.
  return options.some((o) => o.value === value)
    ? listed
    : [{ value: String(value), label: label(value) }, ...listed];
}

/**
 * The Encoder section's selects. `codecs` are the video codecs this browser can
 * send; the codec select is left out when there is nothing to choose between.
 */
export function encoderSettingRows(
  profile: QualityProfile,
  overrides: EncoderOverrides | undefined,
  codecs: Exclude<VideoCodecPreference, "auto">[],
  autoLabel: string
): EncoderSettingRow[] {
  const values = encoderSettingValues(profile, overrides);
  const row = (
    setting: EncoderSetting,
    labelKey: EncoderSettingRow["labelKey"],
    value: string | number,
    options: EncoderOption<string>[]
  ): EncoderSettingRow => ({
    setting,
    labelKey,
    value: String(value),
    options,
    overridden: isEncoderSettingOverridden(overrides, setting),
  });

  const rows: EncoderSettingRow[] = [];
  if (codecs.length > 1 || (values.codec !== "auto" && !codecs.includes(values.codec))) {
    const offered = [...new Set([...codecs, ...(values.codec === "auto" ? [] : [values.codec])])];
    rows.push(
      row("codec", "videoCodec", values.codec, [
        { value: "auto", label: autoLabel },
        ...offered.map((c) => ({ value: c, label: ENCODER_CODEC_LABELS[c] })),
      ])
    );
  }
  rows.push(
    row(
      "resolution",
      "resolution",
      values.resolution,
      choices(ENCODER_RESOLUTION_OPTIONS, values.resolution, (v) => v.replace("x", "×"))
    ),
    row(
      "framerate",
      "framerate",
      values.framerate,
      choices(ENCODER_FRAMERATE_OPTIONS, values.framerate, (v) => `${v} fps`)
    ),
    row(
      "videoBitrate",
      "videoBitrate",
      values.videoBitrate,
      choices(ENCODER_VIDEO_BITRATE_OPTIONS, values.videoBitrate, formatRate)
    ),
    row(
      "audioBitrate",
      "audioBitrate",
      values.audioBitrate,
      choices(ENCODER_AUDIO_BITRATE_OPTIONS, values.audioBitrate, formatRate)
    )
  );
  return rows;
}

/** One read-only line of the effective encoder config, as the advanced panel shows it. */
export interface EncoderSummaryRow {
  labelKey:
    | "videoCodec"
    | "resolution"
    | "framerate"
    | "videoBitrate"
    | "audioCodec"
    | "audioBitrate";
  value: string;
  overridden: boolean;
}

/**
 * The encoder config the next stream starts with, for display only. Editing
 * goes through `encoderSettingRows` and `withEncoderSetting` in the Settings
 * Encoder section.
 */
export function encoderSummaryRows(
  profile: QualityProfile,
  overrides: EncoderOverrides | undefined,
  autoLabel: string
): EncoderSummaryRow[] {
  const values = encoderSettingValues(profile, overrides);
  const overridden = (setting: EncoderSetting) => isEncoderSettingOverridden(overrides, setting);
  return [
    {
      labelKey: "videoCodec",
      value: values.codec === "auto" ? autoLabel : ENCODER_CODEC_LABELS[values.codec],
      overridden: overridden("codec"),
    },
    {
      labelKey: "resolution",
      value: values.resolution.replace("x", "×"),
      overridden: overridden("resolution"),
    },
    {
      labelKey: "framerate",
      value: `${values.framerate} fps`,
      overridden: overridden("framerate"),
    },
    {
      labelKey: "videoBitrate",
      value: formatRate(values.videoBitrate),
      overridden: overridden("videoBitrate"),
    },
    {
      labelKey: "audioCodec",
      value: getEncoderSettings(profile).audio.codec,
      overridden: false,
    },
    {
      labelKey: "audioBitrate",
      value: formatRate(values.audioBitrate),
      overridden: overridden("audioBitrate"),
    },
  ];
}

/** What each Encoder setting shows: the override, or else the profile's value. */
export interface EncoderSettingValues {
  resolution: string;
  framerate: number;
  videoBitrate: number;
  audioBitrate: number;
  codec: VideoCodecPreference;
}

export function encoderSettingValues(
  profile: QualityProfile,
  overrides: EncoderOverrides | undefined
): EncoderSettingValues {
  const base = getEncoderSettings(profile);
  const video = overrides?.video;
  return {
    resolution: `${video?.width ?? base.video.width}x${video?.height ?? base.video.height}`,
    framerate: video?.framerate ?? base.video.framerate,
    videoBitrate: video?.bitrate ?? base.video.bitrate,
    audioBitrate: overrides?.audio?.bitrate ?? base.audio.bitrate,
    codec: video?.codec ?? "auto",
  };
}

/** Whether a setting differs from the profile. */
export function isEncoderSettingOverridden(
  overrides: EncoderOverrides | undefined,
  setting: EncoderSetting
): boolean {
  const video = overrides?.video;
  switch (setting) {
    case "resolution":
      return video?.width !== undefined || video?.height !== undefined;
    case "framerate":
      return video?.framerate !== undefined;
    case "videoBitrate":
      return video?.bitrate !== undefined;
    case "audioBitrate":
      return overrides?.audio?.bitrate !== undefined;
    case "codec":
      return video?.codec !== undefined;
  }
}

function prune(overrides: EncoderOverrides): EncoderOverrides {
  const clean = <T extends object>(part: T | undefined): T | undefined => {
    if (!part) return undefined;
    const kept = Object.fromEntries(Object.entries(part).filter(([, v]) => v !== undefined));
    return Object.keys(kept).length > 0 ? (kept as T) : undefined;
  };
  const video = clean(overrides.video);
  const audio = clean(overrides.audio);
  return { ...(video && { video }), ...(audio && { audio }) };
}

/**
 * The overrides after one Encoder choice. `value` comes from a select, so a
 * number may arrive as a string.
 */
export function withEncoderSetting(
  profile: QualityProfile,
  overrides: EncoderOverrides | undefined,
  setting: EncoderSetting,
  value: string | number
): EncoderOverrides {
  const base = getEncoderSettings(profile);
  const video = { ...overrides?.video };
  const audio = { ...overrides?.audio };
  const numeric = Number(value);

  switch (setting) {
    case "resolution": {
      const [width, height] = String(value).split("x").map(Number);
      const isProfile = width === base.video.width && height === base.video.height;
      video.width = isProfile ? undefined : width;
      video.height = isProfile ? undefined : height;
      break;
    }
    case "framerate":
      video.framerate = numeric === base.video.framerate ? undefined : numeric;
      break;
    case "videoBitrate":
      video.bitrate = numeric === base.video.bitrate ? undefined : numeric;
      break;
    case "audioBitrate":
      audio.bitrate = numeric === base.audio.bitrate ? undefined : numeric;
      break;
    case "codec":
      video.codec = value === "auto" ? undefined : (value as Exclude<VideoCodecPreference, "auto">);
      break;
  }
  return prune({ video, audio });
}
