# Bitrate and Resolution Recommendations

## Industry-Standard Streaming Bitrates

### YouTube Live Recommended Bitrates

Source: https://support.google.com/youtube/answer/2853702

| Resolution | 30 fps        | 60 fps        |
| ---------- | ------------- | ------------- |
| 2160p (4K) | 13,000-34,000 | 20,000-51,000 |
| 1440p      | 6,000-13,000  | 9,000-18,000  |
| 1080p      | 3,000-6,000   | 4,500-9,000   |
| 720p       | 1,500-4,000   | 2,250-6,000   |
| 480p       | 500-2,000     | —             |

All values in Kbps. Codec: H.264. Rate control: CBR. Keyframe interval: 2 seconds (max 4). Audio: AAC at 128 Kbps stereo, 44.1 kHz.

### Twitch Broadcast Requirements

Source: https://help.twitch.tv/s/article/broadcast-guidelines

- Maximum ingest bitrate: 6,000 Kbps
- Video codec: H.264
- Encoding profile: Main (preferred) or Baseline
- Rate control: strict CBR
- Keyframe interval: 2 seconds
- Audio codec: AAC-LC, stereo or mono
- Audio bitrate: 96-160 Kbps
- Supported frame rates: 25, 30, 50, or 60 fps
- GOP size above 10 seconds disables Source quality playback

### General Bitrate Guidelines

For live streaming to any platform, a good starting point:

| Resolution | Frame Rate | Bitrate     |
| ---------- | ---------- | ----------- |
| 1080p      | 60 fps     | 4,500-6,000 |
| 1080p      | 30 fps     | 3,000-4,500 |
| 720p       | 60 fps     | 2,500-4,000 |
| 720p       | 30 fps     | 1,500-3,000 |
| 480p       | 30 fps     | 500-1,500   |

All values in Kbps. These assume H.264 CBR encoding with a 2-second keyframe interval.

## FrameWorks ABR Rendition Ladder

When a Livepeer gateway is available, FrameWorks automatically creates lower-quality renditions from your source stream. The source track is always served as-is (passthrough) alongside the transcoded renditions.

| Rendition | Resolution | Bitrate    | FPS    | H.264 Profile    | Min Input Resolution |
| --------- | ---------- | ---------- | ------ | ---------------- | -------------------- |
| 360p      | 360p       | 900 Kbps   | Source | Constrained High | 640x360              |
| 480p      | 480p       | 1,600 Kbps | Source | Constrained High | 850x480              |
| 720p      | 720p       | 3,200 Kbps | Source | Constrained High | 1,280x720            |
| 1080p     | 1080p      | 6,500 Kbps | Source | Constrained High | 1,920x1080           |

Audio is always transcoded to both Opus (120 Kbps, for WebRTC playback) and AAC (for HLS/native playback) alongside the video renditions.

### What This Means in Practice

A 1080p60 stream at 8,000 Kbps produces five playback options:

1. Source: 1080p at 60 fps, 8,000 Kbps (untranscoded passthrough)
2. 1080p: 1080p at source fps, 6,500 Kbps
3. 720p: 720p at source fps, 3,200 Kbps
4. 480p: 480p at source fps, 1,600 Kbps
5. 360p: 360p at source fps, 900 Kbps

A 720p30 stream at 4,500 Kbps produces four playback options:

1. Source: 720p at 30 fps, 4,500 Kbps (passthrough)
2. 720p: 720p at source fps, 3,200 Kbps
3. 480p: 480p at source fps, 1,600 Kbps
4. 360p: 360p at source fps, 900 Kbps

A 480p stream produces 480p and 360p renditions. A 360p stream produces a 360p rendition. Lower-resolution streams are served as source-only.

### Recommended Ingest Settings for FrameWorks

- Resolution: 1080p or 720p (higher produces better ABR results)
- Frame rate: 30 fps (sufficient for most content) or 60 fps (gaming, sports)
- Bitrate: 3,000-6,000 Kbps for 1080p, 1,500-3,000 Kbps for 720p
- Rate control: CBR
- Keyframe interval: 2 seconds
- B-frames: 0 (required for WebRTC source playback)
