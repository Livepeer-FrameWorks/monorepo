---
"@livepeer-frameworks/streamcrafter-core": minor
"@livepeer-frameworks/streamcrafter-react": minor
"@livepeer-frameworks/streamcrafter-svelte": minor
"@livepeer-frameworks/streamcrafter-wc": minor
---

StreamCrafter no longer records in the browser; FrameWorks records on the server. Removed from core: `RecordingManager`, `WebMWriter` and their option types; on the controller and `createStreamCrafter`, `startRecording()`, `stopRecording()`, `pauseRecording()`, `resumeRecording()`, the `recording`, `recordingDuration` and `recordingFileSize` getters and the controller's `isRecordingActive()`, `getRecordingDuration()` and `getRecordingFileSize()`; the `recordingStarted`, `recordingStopped`, `recordingPaused`, `recordingResumed` and `recordingProgress` events; and `recording` from the reactive state. The React, Svelte and Web Component wrappers expose the controller through `getController()`, so the same members are gone there. `IngestMetadata.recordingEnabled` stays: it reports the stream's server-side recording setting.
