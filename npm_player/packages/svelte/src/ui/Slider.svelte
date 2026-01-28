<script lang="ts">
  import { Slider as BitsSlider } from "bits-ui";
  import { cn } from "@livepeer-frameworks/player-core";

  type $$Props = {
    min?: number;
    max?: number;
    step?: number;
    value?: number;
    orientation?: "horizontal" | "vertical";
    className?: string;
    showTrack?: boolean;
    trackClassName?: string;
    thumbClassName?: string;
    hoverThumb?: boolean;
    accentColor?: boolean;
    oninput?: (value: number) => void;
  } & Record<string, any>;

  let {
    min = 0,
    max = 100,
    step = 1,
    value = 0,
    orientation = "horizontal",
    className = "",
    trackClassName = "",
    thumbClassName = "",
    showTrack = true,
    hoverThumb: _hoverThumb = false,
    accentColor = false,
    oninput = undefined,
    ...rest
  }: $$Props = $props();

  function handleValueChange(newValue: number) {
    value = newValue;
    if (oninput) {
      if (typeof newValue === "number" && Number.isFinite(newValue)) {
        oninput(newValue);
      }
    }
  }
</script>

<BitsSlider.Root
  type="single"
  {min}
  {max}
  {step}
  {value}
  onValueChange={handleValueChange}
  {orientation}
  class={cn("fw-slider", orientation === "vertical" && "fw-slider--vertical", className)}
  {...rest}
>
  {#if showTrack}
    <div class={cn("fw-slider-track", trackClassName)}>
      <BitsSlider.Range class={cn("fw-slider-range", accentColor && "fw-slider-range--accent")} />
    </div>
  {/if}
  <BitsSlider.Thumb
    index={0}
    class={cn("fw-slider-thumb", accentColor && "fw-slider-thumb--accent", thumbClassName)}
  />
</BitsSlider.Root>
