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

  // Colors based on accentColor prop
  const rangeColorClass = $derived(accentColor ? "bg-[hsl(var(--tn-cyan,195_100%_50%))]" : "bg-white/90");
  const thumbColorClass = $derived(accentColor ? "bg-[hsl(var(--tn-cyan,195_100%_50%))]" : "bg-white");

  function handleValueChange(newValue: number) {
    value = newValue;
    if (oninput) {
      // Defensive: ensure we pass a valid finite number (prevents NaN propagation)
      if (typeof newValue === 'number' && Number.isFinite(newValue)) {
        oninput(newValue);
      }
    }
  }
</script>

<div
  class={cn(
    "group relative flex touch-none select-none items-center cursor-pointer",
    orientation === "horizontal" ? "w-full h-5" : "h-full flex-col w-5",
    className
  )}
>
  <BitsSlider.Root
    type="single"
    {min}
    {max}
    {step}
    {value}
    onValueChange={handleValueChange}
    {orientation}
    class="w-full h-full relative flex items-center"
    {...rest}
  >
    {#if showTrack}
      <div
        class={cn(
          "absolute rounded-full bg-white/30 transition-all duration-150",
          orientation === "horizontal"
            ? "inset-x-0 h-1 group-hover:h-1.5"
            : "inset-y-0 w-1 group-hover:w-1.5",
          trackClassName
        )}
      >
        <BitsSlider.Range
          class={cn(
            "absolute rounded-full transition-all duration-150",
            orientation === "horizontal" ? "h-full" : "w-full bottom-0",
            rangeColorClass
          )}
        />
      </div>
    {/if}
    <BitsSlider.Thumb
      index={0}
      class={cn(
        "block rounded-full border-0 cursor-pointer shadow-md transition-all duration-150",
        "w-2.5 h-2.5 group-hover:w-3.5 group-hover:h-3.5",
        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/50",
        "disabled:pointer-events-none disabled:opacity-50",
        thumbColorClass,
        thumbClassName
      )}
    />
  </BitsSlider.Root>
</div>
