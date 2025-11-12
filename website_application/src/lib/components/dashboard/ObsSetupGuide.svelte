<script lang="ts">
  interface Props {
    streamKey: string | null;
  }

  let { streamKey }: Props = $props();

  let displayStreamKey = $derived(
    streamKey ? `${streamKey.slice(0, 12)}...` : "Create a stream first"
  );
</script>

<div class="mt-8">
  <div class="card">
    <div class="card-header">
      <h2 class="text-xl font-semibold text-tokyo-night-fg mb-2">
        New to Streaming?
      </h2>
      <p class="text-tokyo-night-fg-dark">
        Quick setup guide for streaming with OBS
      </p>
    </div>

    <div class="space-y-4">
      <!-- Step 1 -->
      <div class="flex items-start space-x-4">
        <div
          class="w-8 h-8 bg-tokyo-night-cyan text-tokyo-night-bg font-bold rounded-full flex items-center justify-center text-sm"
        >
          1
        </div>
        <div>
          <h3 class="font-semibold text-tokyo-night-fg">
            Get OBS Studio (It's Free!)
          </h3>
          <p class="text-sm text-tokyo-night-fg-dark">
            The most popular streaming software - works on any computer
          </p>
          <a
            href="https://obsproject.com/"
            target="_blank"
            class="text-tokyo-night-cyan hover:underline text-sm"
          >
            Download OBS Studio →
          </a>
        </div>
      </div>

      <!-- Step 2 -->
      <div class="flex items-start space-x-4">
        <div
          class="w-8 h-8 bg-tokyo-night-cyan text-tokyo-night-bg font-bold rounded-full flex items-center justify-center text-sm"
        >
          2
        </div>
        <div>
          <h3 class="font-semibold text-tokyo-night-fg">
            Connect OBS to FrameWorks
          </h3>
          <p class="text-sm text-tokyo-night-fg-dark mb-2">
            Just copy these settings into OBS (Settings → Stream):
          </p>
          <div
            class="bg-tokyo-night-bg-highlight p-3 rounded border border-tokyo-night-fg-gutter"
          >
            <p class="text-xs text-tokyo-night-comment">Server URL:</p>
            <p class="font-mono text-sm text-tokyo-night-fg">
              rtmp://localhost:1935/live
            </p>
            <p class="text-xs text-tokyo-night-comment mt-2">Stream Key:</p>
            <p class="font-mono text-sm text-tokyo-night-fg">
              {displayStreamKey}
            </p>
          </div>
        </div>
      </div>

      <!-- Step 3 -->
      <div class="flex items-start space-x-4">
        <div
          class="w-8 h-8 bg-tokyo-night-cyan text-tokyo-night-bg font-bold rounded-full flex items-center justify-center text-sm"
        >
          3
        </div>
        <div>
          <h3 class="font-semibold text-tokyo-night-fg">Start Streaming</h3>
          <p class="text-sm text-tokyo-night-fg-dark">
            Click "Start Streaming" in OBS to begin broadcasting
          </p>
        </div>
      </div>
    </div>
  </div>
</div>
