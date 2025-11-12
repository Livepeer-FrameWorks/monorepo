<script lang="ts">
  import { getIconComponent } from "$lib/iconUtils";

  const MonitorIcon = getIconComponent("Monitor");
  const KeyIcon = getIconComponent("Key");
  const VideoIcon = getIconComponent("Video");
</script>

<div class="card">
  <div class="card-header">
    <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
      Quick Setup Guide
    </h2>
    <p class="text-tokyo-night-fg-dark">Get started streaming in minutes</p>
  </div>

  <div class="grid grid-cols-1 md:grid-cols-3 gap-6">
    <div class="text-center">
      <div class="text-3xl mb-3">
        <MonitorIcon class="w-8 h-8 text-tokyo-night-purple mx-auto" />
      </div>
      <h3 class="font-semibold text-tokyo-night-fg mb-2">
        1. Configure Software
      </h3>
      <p class="text-sm text-tokyo-night-comment">
        Copy the RTMP URL into OBS Studio, XSplit, or your preferred streaming
        software
      </p>
    </div>

    <div class="text-center">
      <div class="text-3xl mb-3">
        <KeyIcon class="w-8 h-8 text-tokyo-night-yellow mx-auto" />
      </div>
      <h3 class="font-semibold text-tokyo-night-fg mb-2">2. Add Stream Key</h3>
      <p class="text-sm text-tokyo-night-comment">
        Paste your unique stream key to authenticate your broadcast
      </p>
    </div>

    <div class="text-center">
      <div class="text-3xl mb-3">
        <VideoIcon class="w-8 h-8 text-tokyo-night-cyan mx-auto" />
      </div>
      <h3 class="font-semibold text-tokyo-night-fg mb-2">3. Start Streaming</h3>
      <p class="text-sm text-tokyo-night-comment">
        Hit "Start Streaming" and share your HLS playback URL with viewers
      </p>
    </div>
  </div>
</div>
