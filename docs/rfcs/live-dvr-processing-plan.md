# Live-DVR Processing Plan

## Current Decision

- Do not set `dvr.realtime=true` as a workaround.
- Keep `dvr+<internal>` as `INPUT:HLS` over the HLS EVENT playlist.
- Put rolling-DVR thumbnails behind a proper Mist change instead of projecting live thumbnails onto the DVR buffer.
- For now, disable/omit rolling-DVR thumbnails at the product/config layer until Mist can process this path correctly.
- Keep finalized DVR chapter thumbnails on the existing `processing+<hash>` path.

## Problem

`dvr+<internal>` is a live-DVR playback surface with a long HLS EVENT buffer. In Mist, that path is handled by `MistInHLS`, not by the same `InputBuffer` process-controller path used by normal live processing.

`STREAM_PROCESS` is currently emitted from `InputBuffer::userLeadIn()`. An HLS EVENT input can be both live-like and seekable, but that does not mean it automatically enters the input-buffer process controller. Returning a `Thumbs` config for `dvr+` is therefore not enough.

Forcing `realtime=true` is risky because it changes how Mist opens and paces the HLS input. It may make the stream go through the input-buffer machinery, but it also risks losing the efficient HLS EVENT buffering/seeking behavior that live DVR needs.

## Goals

- Support normal live processing config for live DVR, not a DVR-only thumbnails path.
- Allow `Thumbs`, audio transcode, video transcode, and future process types to run on `dvr+`.
- Start processing at the beginning of the currently retained DVR window.
- Catch up to the live point at a bounded/adaptive speed so a long DVR window does not blitz through hours of segments at maximum speed.
- Once caught up, behave like normal live processing.
- Preserve HLS EVENT seeking, segment parsing, live-point tracking, and retention-window behavior.

## Non-Goals

- No `dvr.realtime=true` base config workaround.
- No live thumbnail cache projection onto DVR.
- No thumbnails-only special processing lane for DVR.
- No full reimplementation of `InputBuffer` inside `MistInHLS`.

## Mist Trace To Verify

- `MistInHLS` owns HLS EVENT playlist parsing and treats it as live-like and seekable.
- Non-realtime HLS uses HLS-specific live parsing and segment buffering rather than the generic input-buffer process controller.
- `STREAM_PROCESS` is emitted from the input-buffer controller path.
- Existing MistProc source readers can read historical media too fast for long DVR windows unless an explicit pacing/start policy is added.

## Proposed Mist Design

1. Extract the process supervision logic from `InputBuffer` into a reusable process-controller component.
2. Keep `InputBuffer` behavior unchanged by wiring it to the extracted controller.
3. Add an HLS EVENT attachment point in `MistInHLS` that can use the same controller without switching the input to realtime mode.
4. Add a DVR-aware processing start/pacing policy:
   - start at the first available packet/keyframe in the current DVR window;
   - catch up to the live point with a configurable cap or adaptive throttle;
   - switch to live-follow once caught up.
5. Keep process config resolution through `STREAM_PROCESS` so Foghorn can return per-instance config for `dvr+<internal>`.
6. Reuse existing process lifecycle behavior: inhibit rules, start/stop/restart, backoff, process SHM cleanup, `PROCESS_EXIT`, stats, and pressure accounting.

## Control-Plane Work

- Short term: omit rolling-DVR thumbnail assets and do not advertise broken DVR sprite/scrub thumbnails.
- Gate `dvr+` process config behind a Mist capability/version flag.
- When the capability is absent, return no process config for `dvr+`.
- When the capability is present, stamp/return the tenant's normal live process config for the DVR lane.
- Keep finalized chapter processing on `processing+<hash>`.

## Tests

- Mist: HLS EVENT `dvr+` attaches the process controller without enabling realtime mode.
- Mist: processing starts at the beginning of the DVR window, catches up under the speed cap, then follows live.
- Mist: `Thumbs`, audio transcode, and video transcode all work from the same config model.
- Mist: DVR seek behavior and retention-window trimming stay unchanged with processing enabled.
- Foghorn: `dvr+` returns no process config unless the Mist capability gate is enabled.
- Playback: rolling DVR does not expose sprite/scrub thumbnail assets while the gate is disabled.

## Rollout

1. Disable/omit broken rolling-DVR thumbnail exposure.
2. Build the Mist process-controller extraction and HLS EVENT attachment behind a feature flag.
3. Add the Foghorn capability gate.
4. Enable on one canary cluster with a long DVR window.
5. Re-enable DVR thumbnail assets after end-to-end processing, upload, playback, and scrubbing are verified.
