<script lang="ts">
  import type { PlayerController } from "@livepeer-frameworks/player-core";
  import Player from "../../src/Player.svelte";

  let {
    contentId,
    onController,
  }: { contentId: string; onController: (controller: PlayerController | null) => void } = $props();

  let controller = $state<PlayerController | null>(null);
  $effect(() => onController(controller));
</script>

<Player
  {contentId}
  contentType="live"
  options={{ mistUrl: "https://edge.example" }}
  bind:controller
/>
