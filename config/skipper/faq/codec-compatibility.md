# Codec Compatibility and WebRTC Requirements

## H.264 Profiles and WebRTC

### WebRTC Mandatory Codec Support

Source: RFC 7742 (https://www.rfc-editor.org/rfc/rfc7742.html)

WebRTC endpoints MUST support:

- H.264 Constrained Baseline Profile Level 1.2
- VP8

WebRTC endpoints SHOULD support:

- H.264 Constrained High Profile Level 1.3

### B-Frames and WebRTC

B-frames (bidirectional predicted frames) are NOT supported in WebRTC playback. The Constrained Baseline Profile used by WebRTC excludes B-frames by definition.

Source: RFC 7742 + MDN WebRTC Codecs (https://developer.mozilla.org/en-US/docs/Web/Media/Guides/Formats/WebRTC_codecs)

Why this matters for FrameWorks:

- The source track is served via WebRTC alongside ABR renditions
- If the source contains B-frames, WebRTC playback of the source track fails to decode
- ABR renditions transcoded by Livepeer use H264 Constrained High but do not add B-frames for live content
- Only the source (passthrough) track is affected

How to fix:

- OBS Studio: Output > Encoder settings > set B-frames (bf) to 0
- FFmpeg: add `-bf 0` to the encoding command
- Alternatively, use the H.264 Baseline profile which excludes B-frames by definition
- Browser ingest via StreamCrafter SDK (WHIP) uses WebRTC natively, so B-frames are never an issue

## Audio Codec Behavior on FrameWorks

FrameWorks automatically handles audio codec transcoding between AAC and Opus:

- AAC input: an Opus copy at 120 Kbps is created for WebRTC listeners. The original AAC track is preserved for HLS/DASH playback.
- Opus input: an AAC copy is created for HLS/DASH listeners. The original Opus track is preserved for WebRTC playback.

This transcoding is automatic and requires no user configuration. Both audio tracks are always available regardless of input codec.

## HLS Segment Duration

### RFC 8216 (IETF Standard)

Source: https://www.rfc-editor.org/rfc/rfc8216.html

- Typical target duration: 10 seconds (Section 6.2.1)
- The EXTINF duration, when rounded to nearest integer, MUST be less than or equal to EXT-X-TARGETDURATION
- Minimum live playlist duration: 3 times the target duration
- Client playlist refresh interval: between 0.5x and 1.5x target duration

### Apple HLS Authoring Specification

Source: https://developer.apple.com/documentation/http-live-streaming/hls-authoring-specification-for-apple-devices

- Recommended target duration: 6 seconds (for both fMP4 and MPEG-TS)
- Video segments MUST start with an IDR frame (instantaneous decoder refresh)
- Keyframe interval must match or divide evenly into segment duration

### Practical Impact

- Shorter segments (2-4s) reduce latency but increase overhead and may cause more rebuffering on slow connections
- Longer segments (6-10s) improve reliability but increase glass-to-glass latency
- Keyframe interval MUST align with segment duration. A 2-second keyframe interval works with 2s, 4s, 6s, or 10s segments. A 3-second keyframe interval only works cleanly with 3s, 6s, or 9s segments.
