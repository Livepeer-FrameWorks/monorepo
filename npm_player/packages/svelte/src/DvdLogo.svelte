<!--
  DvdLogo.svelte - Bouncing DVD logo animation
  Port of src/components/DvdLogo.tsx
-->
<script lang="ts">
  import { onMount, onDestroy } from "svelte";

  interface Props {
    parentRef: HTMLElement | undefined;
    scale?: number;
  }

  let { parentRef, scale = 0.15 }: Props = $props();

  type Point = { top: number; left: number };
  type Velocity = { x: number; y: number };
  type Size = { width: number; height: number };

  const ORIGINAL_WIDTH = 153;
  const ORIGINAL_HEIGHT = 69;
  const ASPECT_RATIO = ORIGINAL_WIDTH / ORIGINAL_HEIGHT;

  const COLORS = [
    "#7aa2f7",
    "#bb9af7",
    "#9ece6a",
    "#73daca",
    "#7dcfff",
    "#f7768e",
    "#e0af68",
    "#2ac3de",
  ];

  function pickNextColor(current?: string): string {
    if (COLORS.length === 0) return current ?? "#ffffff";
    if (COLORS.length === 1) return COLORS[0];

    let next: string;
    do {
      next = COLORS[Math.floor(Math.random() * COLORS.length)];
    } while (next === current);

    return next;
  }

  // State
  let position = $state<Point>({ top: 0, left: 0 });
  let dimensions = $state<Size>({ width: ORIGINAL_WIDTH, height: ORIGINAL_HEIGHT });
  let color = $state<string>(pickNextColor());

  // Refs (non-reactive mutable state for animation)
  let positionRef: Point = { top: 0, left: 0 };
  let dimensionsRef: Size = { width: ORIGINAL_WIDTH, height: ORIGINAL_HEIGHT };
  let velocityRef: Velocity = { x: 1.8, y: 1.6 };
  let animationFrame: number | null = null;
  let resizeObserver: ResizeObserver | null = null;
  let lastTimestamp = performance.now();

  function recalculateDimensions() {
    if (!parentRef) return;

    const parentWidth = parentRef.clientWidth;
    const parentHeight = parentRef.clientHeight;
    if (parentWidth === 0 || parentHeight === 0) return;

    const maxWidth = parentWidth * scale;
    const maxHeight = parentHeight * scale;

    let width = maxWidth;
    let height = width / ASPECT_RATIO;

    if (height > maxHeight) {
      height = maxHeight;
      width = height * ASPECT_RATIO;
    }

    const nextDimensions: Size = {
      width: Math.max(20, width),
      height: Math.max(20, height),
    };

    dimensionsRef = nextDimensions;
    dimensions = nextDimensions;

    const maxTop = Math.max(0, parentHeight - nextDimensions.height);
    const maxLeft = Math.max(0, parentWidth - nextDimensions.width);

    const startPosition: Point = {
      top: Math.random() * maxTop,
      left: Math.random() * maxLeft,
    };

    positionRef = startPosition;
    position = startPosition;

    const baseSpeed = Math.max(1.2, Math.min(nextDimensions.width, nextDimensions.height) / 70);
    velocityRef = {
      x: baseSpeed * (Math.random() > 0.5 ? 1 : -1),
      y: baseSpeed * (Math.random() > 0.5 ? 1 : -1),
    };
  }

  function animate(timestamp: number) {
    if (!parentRef) {
      animationFrame = requestAnimationFrame(animate);
      return;
    }

    const dims = dimensionsRef;
    if (dims.width === 0 || dims.height === 0) {
      animationFrame = requestAnimationFrame(animate);
      return;
    }

    const deltaMs = timestamp - lastTimestamp;
    lastTimestamp = timestamp;
    // Cap multiplier to prevent huge jumps on lag spikes (match React)
    const speedMultiplier = Math.min(deltaMs / 16, 2);

    const maxTop = parentRef.clientHeight - dims.height;
    const maxLeft = parentRef.clientWidth - dims.width;

    let { top, left } = positionRef;
    let { x, y } = velocityRef;
    let bounced = false;

    top += y * speedMultiplier;
    left += x * speedMultiplier;

    if (top <= 0 || top >= maxTop) {
      y = -y;
      top = Math.max(0, Math.min(maxTop, top));
      bounced = true;
    }

    if (left <= 0 || left >= maxLeft) {
      x = -x;
      left = Math.max(0, Math.min(maxLeft, left));
      bounced = true;
    }

    velocityRef = { x, y };
    positionRef = { top, left };
    position = { top, left };

    if (bounced) {
      color = pickNextColor(color);
    }

    animationFrame = requestAnimationFrame(animate);
  }

  // Setup resize observer and animation when parentRef changes
  $effect(() => {
    if (!parentRef) return;

    recalculateDimensions();

    if (typeof ResizeObserver !== "undefined") {
      resizeObserver = new ResizeObserver(() => recalculateDimensions());
      resizeObserver.observe(parentRef);
    } else {
      const onResize = () => recalculateDimensions();
      window.addEventListener("resize", onResize);
      return () => window.removeEventListener("resize", onResize);
    }

    return () => {
      resizeObserver?.disconnect();
      resizeObserver = null;
    };
  });

  // Start animation on mount
  onMount(() => {
    animationFrame = requestAnimationFrame(animate);
  });

  // Cleanup on destroy
  onDestroy(() => {
    if (animationFrame) {
      cancelAnimationFrame(animationFrame);
      animationFrame = null;
    }
    resizeObserver?.disconnect();
    resizeObserver = null;
  });
</script>

<div
  class="fw-player-dvd"
  style="
    position: absolute;
    pointer-events: none;
    user-select: none;
    top: {position.top}px;
    left: {position.left}px;
    width: {dimensions.width}px;
    height: {dimensions.height}px;
  "
>
  <svg width="100%" height="100%" viewBox="0 0 153 69" fill={color} class="select-none">
    <g>
      <path
        d="M140.186,63.52h-1.695l-0.692,5.236h-0.847l0.77-5.236h-1.693l0.076-0.694h4.158L140.186,63.52L140.186,63.52z M146.346,68.756h-0.848v-4.545l0,0l-2.389,4.545l-1-4.545l0,0l-1.462,4.545h-0.771l1.924-5.931h0.695l0.924,4.006l2.078-4.006 h0.848V68.756L146.346,68.756z M126.027,0.063H95.352c0,0-8.129,9.592-9.654,11.434c-8.064,9.715-9.523,12.32-9.779,13.02 c0.063-0.699-0.256-3.304-3.686-13.148C71.282,8.7,68.359,0.062,68.359,0.062H57.881V0L32.35,0.063H13.169l-1.97,8.131 l14.543,0.062h3.365c9.336,0,15.055,3.747,13.467,10.354c-1.717,7.24-9.91,10.416-18.545,10.416h-3.24l4.191-17.783H10.502 L4.34,37.219h20.578c15.432,0,30.168-8.13,32.709-18.608c0.508-1.906,0.443-6.67-0.764-9.527c0-0.127-0.063-0.191-0.127-0.444 c-0.064-0.063-0.127-0.509,0.127-0.571c0.128-0.062,0.383,0.189,0.445,0.254c0.127,0.317,0.19,0.57,0.19,0.57l13.083,36.965 l33.344-37.6h14.1h3.365c9.337,0,15.055,3.747,13.528,10.354c-1.778,7.24-9.972,10.416-18.608,10.416h-3.238l4.191-17.783h-14.481 l-6.159,25.976h20.576c15.434,0,30.232-8.13,32.709-18.608C152.449,8.193,141.523,0.063,126.027,0.063L126.027,0.063z M71.091,45.981c-39.123,0-70.816,4.512-70.816,10.035c0,5.59,31.693,10.034,70.816,10.034c39.121,0,70.877-4.444,70.877-10.034 C141.968,50.493,110.212,45.981,71.091,45.981L71.091,45.981z M68.55,59.573c-8.956,0-16.196-1.523-16.196-3.365 c0-1.84,7.239-3.303,16.196-3.303c8.955,0,16.195,1.463,16.195,3.303C84.745,58.050,77.505,59.573,68.55,59.573L68.55,59.573z"
      />
    </g>
  </svg>
</div>
