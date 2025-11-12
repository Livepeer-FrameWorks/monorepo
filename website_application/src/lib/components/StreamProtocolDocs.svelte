<!-- Protocol documentation component -->
<script lang="ts">
  interface Props {
    hostname?: string;
    port?: string | number;
    streamName?: string;
  }

  const defaultHostname =
    typeof window !== "undefined" ? window.location.hostname : "localhost";
  const defaultPort =
    typeof window !== "undefined" ? window.location.port : "";

  let {
    hostname = defaultHostname,
    port = defaultPort,
    streamName = "",
  }: Props = $props();

  let baseUrl = $derived(`${hostname}${port ? `:${String(port)}` : ""}`);
</script>

<div class="bg-white dark:bg-gray-800 rounded-lg shadow p-6">
  <h2 class="text-2xl font-bold mb-4">Ingest Protocols</h2>
  <div class="space-y-4">
    <div class="border-b pb-4">
      <h3 class="font-semibold text-lg">SRT</h3>
      <code class="block bg-gray-100 dark:bg-gray-900 p-2 rounded">
        srt://{baseUrl}:1935?streamid={streamName}&latency=200&mode=caller&passphrase=your_passphrase
      </code>
    </div>
    
    <div class="border-b pb-4">
      <h3 class="font-semibold text-lg">WHIP (WebRTC)</h3>
      <code class="block bg-gray-100 dark:bg-gray-900 p-2 rounded">
        https://{baseUrl}/webrtc/{streamName}
      </code>
    </div>
    
    <div class="border-b pb-4">
      <h3 class="font-semibold text-lg">RTMP/RTMPS</h3>
      <code class="block bg-gray-100 dark:bg-gray-900 p-2 rounded">
        rtmp://{baseUrl}/live/{streamName}<br>
        rtmps://{baseUrl}/live/{streamName}
      </code>
    </div>
  </div>

  <h2 class="text-2xl font-bold mt-8 mb-4">Playback Protocols</h2>
  <div class="space-y-4">
    <div class="border-b pb-4">
      <h3 class="font-semibold text-lg">WHEP (WebRTC)</h3>
      <code class="block bg-gray-100 dark:bg-gray-900 p-2 rounded">
        https://{baseUrl}/webrtc/{streamName}
      </code>
    </div>
    
    <div class="border-b pb-4">
      <h3 class="font-semibold text-lg">LL-HLS/CMAF</h3>
      <code class="block bg-gray-100 dark:bg-gray-900 p-2 rounded">
        https://{baseUrl}/cmaf/{streamName}/index.m3u8
      </code>
    </div>
    
    <div class="border-b pb-4">
      <h3 class="font-semibold text-lg">DASH</h3>
      <code class="block bg-gray-100 dark:bg-gray-900 p-2 rounded">
        https://{baseUrl}/cmaf/{streamName}/index.mpd
      </code>
    </div>
    
    <div class="border-b pb-4">
      <h3 class="font-semibold text-lg">SRT</h3>
      <code class="block bg-gray-100 dark:bg-gray-900 p-2 rounded">
        srt://{baseUrl}:1935?streamid={streamName}
      </code>
    </div>
    
    <div class="border-b pb-4">
      <h3 class="font-semibold text-lg">HLS</h3>
      <code class="block bg-gray-100 dark:bg-gray-900 p-2 rounded">
        http://{baseUrl}/hls/{streamName}/index.m3u8
      </code>
    </div>
    
    <div class="border-b pb-4">
      <h3 class="font-semibold text-lg">WebSocket/MP4</h3>
      <p class="text-sm text-gray-600 dark:text-gray-400">Available via MistMetaPlayer integration</p>
    </div>
    
    <div>
      <h3 class="font-semibold text-lg">MKV</h3>
      <code class="block bg-gray-100 dark:bg-gray-900 p-2 rounded">
        http://{baseUrl}/mkv/{streamName}
      </code>
    </div>
  </div>
</div> 
