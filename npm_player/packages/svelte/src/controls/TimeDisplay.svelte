<script lang="ts">
  import { getContext } from "svelte";
  import { formatTimeDisplay } from "@livepeer-frameworks/player-core";

  let pc: any = getContext("fw-player-controller");

  let timeText = $derived(
    formatTimeDisplay({
      isLive: pc?.isEffectivelyLive ?? false,
      currentTime: pc?.currentTime ?? 0,
      duration: pc?.duration ?? NaN,
      liveEdge: pc?.duration ?? 0,
      seekableStart: 0,
    })
  );
</script>

<span class="fw-time-display">{timeText}</span>
