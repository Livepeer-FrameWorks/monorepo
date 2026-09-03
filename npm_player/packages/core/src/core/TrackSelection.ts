import type { StreamTrack } from "./PlayerInterface";

export interface SelectableMediaTrack {
  id: string;
  label: string;
  lang?: string;
  active: boolean;
}

export interface DesiredTrackSelection {
  videoId: string;
  videoExplicit: boolean;
  audioId: string | null;
}

export interface TrackSelectionReplay {
  tracks: { video?: string; audio?: string };
}

export function buildTrackSelectionReplay(selection: DesiredTrackSelection): TrackSelectionReplay {
  const tracks: TrackSelectionReplay["tracks"] = {};
  if (selection.videoExplicit) tracks.video = selection.videoId;
  if (selection.audioId !== null) tracks.audio = selection.audioId;
  return { tracks };
}

function trackID(track: StreamTrack): string | null {
  if (track.id?.trim()) return track.id;
  if (track.idx !== undefined) return String(track.idx);
  return null;
}

function languageLabel(lang: string | undefined): string | undefined {
  if (!lang || lang === "und") return undefined;
  try {
    return new Intl.DisplayNames(undefined, { type: "language" }).of(lang) ?? lang.toUpperCase();
  } catch {
    return lang.toUpperCase();
  }
}

export function buildAudioTrackList(
  tracks: StreamTrack[] | null | undefined,
  selectedID: string | null = null
): SelectableMediaTrack[] {
  const audio = (tracks ?? []).filter((track) => track.type === "audio");
  return audio.flatMap((track, position) => {
    const id = trackID(track);
    if (id === null) return [];
    const language = languageLabel(track.lang);
    return [
      {
        id,
        label: language ?? `Audio ${position + 1}`,
        lang: track.lang,
        active: selectedID === id,
      },
    ];
  });
}

export function buildSubtitleTrackList(
  tracks: StreamTrack[] | null | undefined,
  selectedID: string | null = null
): SelectableMediaTrack[] {
  const subtitles = (tracks ?? []).filter(
    (track) => track.type === "meta" && track.codec?.toLowerCase() === "subtitle"
  );
  return subtitles.flatMap((track, position) => {
    const id = trackID(track);
    if (id === null) return [];
    const language = languageLabel(track.lang);
    return [
      {
        id,
        label: language ?? `Subtitles ${position + 1}`,
        lang: track.lang,
        active: selectedID === id,
      },
    ];
  });
}
