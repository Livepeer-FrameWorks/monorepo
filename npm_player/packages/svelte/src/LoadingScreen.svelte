<!--
  LoadingScreen.svelte - Animated loading screen with Tokyo Night theme
  Port of src/components/LoadingScreen.tsx

  Features:
  - Animated colored bubbles with Tokyo Night palette
  - Center logo with mouse-tracking "push away" effect
  - Bouncing DVD logo component
  - Hitmarker sound effects (Web Audio API synthesis)
  - Floating particles with gradients
  - Pulsing circle around logo
  - Animated background gradient shifts
-->
<script lang="ts">
  import { onMount, onDestroy, getContext } from "svelte";
  import { readable } from "svelte/store";
  import type { Readable } from "svelte/store";
  import { createTranslator, type TranslateFn } from "@livepeer-frameworks/player-core";
  import DvdLogo from "./DvdLogo.svelte";
  import logomarkAsset from "./assets/logomark.svg";

  const translatorStore: Readable<TranslateFn> =
    getContext<Readable<TranslateFn> | undefined>("fw-translator") ??
    readable(createTranslator({ locale: "en" }));
  let t: TranslateFn = $derived($translatorStore);

  interface Props {
    message?: string;
    logoSrc?: string;
  }

  let { message = undefined, logoSrc }: Props = $props();
  let effectiveMessage = $derived(message ?? t("waitingForSource"));

  // Use imported asset as default if logoSrc not provided
  let effectiveLogoSrc = $derived(logoSrc || logomarkAsset);

  // Container ref for mouse tracking
  let containerRef: HTMLDivElement | undefined = $state();

  // Hitmarker state
  interface Hitmarker {
    id: number;
    x: number;
    y: number;
  }
  let hitmarkers = $state<Hitmarker[]>([]);

  // Center logo state
  let logoSize = $state(100);
  let offset = $state({ x: 0, y: 0 });
  let isHovered = $state(false);

  // Generate random particles
  const particles = Array.from({ length: 12 }, () => ({
    left: Math.random() * 100,
    size: Math.random() * 4 + 2,
    duration: 8 + Math.random() * 4,
    delay: Math.random() * 8,
  }));

  // Animated bubble component state
  interface BubbleState {
    position: { top: number; left: number };
    size: number;
    opacity: number;
    timeoutId: ReturnType<typeof setTimeout> | null;
  }

  let bubbles = $state<BubbleState[]>(
    Array.from({ length: 8 }, () => ({
      position: { top: Math.random() * 80 + 10, left: Math.random() * 80 + 10 },
      size: Math.random() * 60 + 30,
      opacity: 0,
      timeoutId: null,
    }))
  );

  // Bubble animation cycle
  function animateBubble(index: number) {
    // Fade in
    bubbles[index].opacity = 0.15;

    const visibleDuration = 4000 + Math.random() * 3000; // 4-7 seconds visible

    const timeout1 = setTimeout(() => {
      // Fade out
      bubbles[index].opacity = 0;

      const timeout2 = setTimeout(() => {
        // Move to new random position while invisible
        bubbles[index].position = {
          top: Math.random() * 80 + 10,
          left: Math.random() * 80 + 10,
        };
        bubbles[index].size = Math.random() * 60 + 30;

        const timeout3 = setTimeout(() => {
          animateBubble(index);
        }, 200);
        bubbles[index].timeoutId = timeout3;
      }, 1500); // Wait for fade out
      bubbles[index].timeoutId = timeout2;
    }, visibleDuration);
    bubbles[index].timeoutId = timeout1;
  }

  // Hitmarker sound synthesis
  function createSyntheticHitmarkerSound() {
    try {
      const audioContext = new (window.AudioContext || (window as any).webkitAudioContext)();

      const oscillator1 = audioContext.createOscillator();
      const oscillator2 = audioContext.createOscillator();
      const noiseBuffer = audioContext.createBuffer(
        1,
        audioContext.sampleRate * 0.1,
        audioContext.sampleRate
      );
      const noiseSource = audioContext.createBufferSource();

      // Generate white noise for the initial "crack"
      const noiseData = noiseBuffer.getChannelData(0);
      for (let i = 0; i < noiseData.length; i++) {
        noiseData[i] = Math.random() * 2 - 1;
      }
      noiseSource.buffer = noiseBuffer;

      const gainNode1 = audioContext.createGain();
      const gainNode2 = audioContext.createGain();
      const noiseGain = audioContext.createGain();
      const masterGain = audioContext.createGain();

      oscillator1.connect(gainNode1);
      oscillator2.connect(gainNode2);
      noiseSource.connect(noiseGain);

      gainNode1.connect(masterGain);
      gainNode2.connect(masterGain);
      noiseGain.connect(masterGain);
      masterGain.connect(audioContext.destination);

      // Sharp metallic frequencies
      oscillator1.frequency.setValueAtTime(1800, audioContext.currentTime);
      oscillator1.frequency.exponentialRampToValueAtTime(900, audioContext.currentTime + 0.08);

      oscillator2.frequency.setValueAtTime(3600, audioContext.currentTime);
      oscillator2.frequency.exponentialRampToValueAtTime(1800, audioContext.currentTime + 0.04);

      oscillator1.type = "triangle";
      oscillator2.type = "sine";

      // Sharp attack, quick decay
      gainNode1.gain.setValueAtTime(0, audioContext.currentTime);
      gainNode1.gain.linearRampToValueAtTime(0.4, audioContext.currentTime + 0.002);
      gainNode1.gain.exponentialRampToValueAtTime(0.001, audioContext.currentTime + 0.12);

      gainNode2.gain.setValueAtTime(0, audioContext.currentTime);
      gainNode2.gain.linearRampToValueAtTime(0.3, audioContext.currentTime + 0.001);
      gainNode2.gain.exponentialRampToValueAtTime(0.001, audioContext.currentTime + 0.06);

      // Noise burst
      noiseGain.gain.setValueAtTime(0, audioContext.currentTime);
      noiseGain.gain.linearRampToValueAtTime(0.2, audioContext.currentTime + 0.001);
      noiseGain.gain.exponentialRampToValueAtTime(0.001, audioContext.currentTime + 0.01);

      masterGain.gain.setValueAtTime(0.5, audioContext.currentTime);

      const startTime = audioContext.currentTime;
      const stopTime = startTime + 0.15;

      oscillator1.start(startTime);
      oscillator2.start(startTime);
      noiseSource.start(startTime);

      oscillator1.stop(stopTime);
      oscillator2.stop(stopTime);
      noiseSource.stop(startTime + 0.02);
    } catch {
      // Audio context not available
    }
  }

  function playHitmarkerSound() {
    try {
      // Embedded hitmarker sound as base64 data URL (matches React implementation)
      const hitmarkerDataUrl =
        "data:audio/mpeg;base64,SUQzBAAAAAAANFRDT04AAAAHAAADT3RoZXIAVFNTRQAAAA8AAANMYXZmNTcuODMuMTAwAAAAAAAAAAAA" +
        "AAD/+1QAAAAAAAAAAAAAAAAAAAAA" +
        "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" +
        "qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqo=";

      const audio = new Audio(hitmarkerDataUrl);
      audio.volume = 0.3;
      audio.play().catch(() => {
        // Fallback to synthetic sound if data URL fails
        createSyntheticHitmarkerSound();
      });
    } catch {
      // Fallback to synthetic sound
      createSyntheticHitmarkerSound();
    }
  }

  function createHitmarker(e: { clientX: number; clientY: number }) {
    if (!containerRef) return;

    const rect = containerRef.getBoundingClientRect();
    const x = e.clientX - rect.left;
    const y = e.clientY - rect.top;

    const newHitmarker: Hitmarker = {
      id: Date.now() + Math.random(),
      x,
      y,
    };

    hitmarkers = [...hitmarkers, newHitmarker];
    playHitmarkerSound();

    // Remove hitmarker after animation
    setTimeout(() => {
      hitmarkers = hitmarkers.filter((h) => h.id !== newHitmarker.id);
    }, 600);
  }

  // Mouse tracking for logo push-away effect
  function handleMouseMove(e: MouseEvent) {
    if (!containerRef) return;

    const rect = containerRef.getBoundingClientRect();
    const centerX = rect.left + rect.width / 2;
    const centerY = rect.top + rect.height / 2;

    const mouseX = e.clientX;
    const mouseY = e.clientY;

    const deltaX = mouseX - centerX;
    const deltaY = mouseY - centerY;
    const distance = Math.sqrt(deltaX * deltaX + deltaY * deltaY);

    const maxDistance = logoSize * 1.5;
    if (distance < maxDistance && distance > 0) {
      const pushStrength = (maxDistance - distance) / maxDistance;
      const pushDistance = 50 * pushStrength;

      offset = {
        x: -(deltaX / distance) * pushDistance,
        y: -(deltaY / distance) * pushDistance,
      };
      isHovered = true;
    } else {
      offset = { x: 0, y: 0 };
      isHovered = false;
    }
  }

  function handleMouseLeave() {
    offset = { x: 0, y: 0 };
    isHovered = false;
  }

  function handleLogoClick(e: MouseEvent) {
    e.stopPropagation();
    createHitmarker({ clientX: e.clientX, clientY: e.clientY });
  }

  // Update logo size on container resize
  $effect(() => {
    if (!containerRef) return;

    const updateLogoSize = () => {
      if (containerRef) {
        const minDimension = Math.min(containerRef.clientWidth, containerRef.clientHeight);
        logoSize = minDimension * 0.2;
      }
    };

    updateLogoSize();

    const resizeObserver = new ResizeObserver(updateLogoSize);
    resizeObserver.observe(containerRef);

    return () => resizeObserver.disconnect();
  });

  // Start bubble animations on mount
  onMount(() => {
    bubbles.forEach((_, index) => {
      setTimeout(() => animateBubble(index), index * 500);
    });
  });

  // Cleanup on destroy
  onDestroy(() => {
    bubbles.forEach((bubble) => {
      if (bubble.timeoutId) clearTimeout(bubble.timeoutId);
    });
  });
</script>

<div
  bind:this={containerRef}
  class="loading-container fw-player-root"
  role="status"
  aria-label={t("loading")}
  onmousemove={handleMouseMove}
  onmouseleave={handleMouseLeave}
>
  <!-- Hitmarkers -->
  {#each hitmarkers as hitmarker (hitmarker.id)}
    <div class="hitmarker" style="left: {hitmarker.x}px; top: {hitmarker.y}px;">
      <div class="hitmarker-line tl"></div>
      <div class="hitmarker-line tr"></div>
      <div class="hitmarker-line bl"></div>
      <div class="hitmarker-line br"></div>
    </div>
  {/each}

  <!-- Floating particles -->
  {#each particles as particle, _i}
    <div
      class="particle"
      style="
        left: {particle.left}%;
        width: {particle.size}px;
        height: {particle.size}px;
        animation-duration: {particle.duration}s;
        animation-delay: {particle.delay}s;
      "
    ></div>
  {/each}

  <!-- Animated bubbles -->
  {#each bubbles as bubble, _i}
    <div
      class="bubble"
      style="
        top: {bubble.position.top}%;
        left: {bubble.position.left}%;
        width: {bubble.size}px;
        height: {bubble.size}px;
        opacity: {bubble.opacity};
      "
    ></div>
  {/each}

  <!-- Center logo with push-away effect -->
  <div
    class="center-logo"
    style="transform: translate(-50%, -50%) translate({offset.x}px, {offset.y}px);"
  >
    <!-- Pulsing circle background -->
    <div
      class="logo-pulse"
      class:hovered={isHovered}
      style="width: {logoSize * 1.4}px; height: {logoSize * 1.4}px;"
    ></div>

    <!-- Logo image -->
    <button type="button" class="logo-button" onclick={handleLogoClick} aria-label="Logo">
      <img
        src={effectiveLogoSrc}
        alt=""
        class="logo-image"
        class:hovered={isHovered}
        style="width: {logoSize}px; height: {logoSize}px;"
        draggable="false"
      />
    </button>
  </div>

  <!-- Bouncing DVD Logo -->
  <DvdLogo parentRef={containerRef} scale={0.08} />

  <!-- Message -->
  <div class="message">
    {effectiveMessage}
  </div>

  <!-- Subtle overlay texture -->
  <div class="overlay-texture"></div>
</div>

<style>
  @keyframes fadeInOut {
    0%,
    100% {
      opacity: 0.6;
    }
    50% {
      opacity: 0.9;
    }
  }

  @keyframes logoPulse {
    0%,
    100% {
      opacity: 0.15;
      transform: scale(1);
    }
    50% {
      opacity: 0.25;
      transform: scale(1.05);
    }
  }

  @keyframes floatUp {
    0% {
      transform: translateY(100vh) rotate(0deg);
      opacity: 0;
    }
    10% {
      opacity: 0.6;
    }
    90% {
      opacity: 0.6;
    }
    100% {
      transform: translateY(-100px) rotate(360deg);
      opacity: 0;
    }
  }

  @keyframes gradientShift {
    0%,
    100% {
      background-position: 0% 50%;
    }
    50% {
      background-position: 100% 50%;
    }
  }

  @keyframes hitmarkerFade {
    0% {
      opacity: 1;
      transform: scale(0.5);
    }
    20% {
      opacity: 1;
      transform: scale(1.2);
    }
    100% {
      opacity: 0;
      transform: scale(1);
    }
  }

  @keyframes hitmarkerFade45 {
    0% {
      opacity: 1;
      transform: translate(-50%, -50%) rotate(45deg) scale(0.5);
    }
    20% {
      opacity: 1;
      transform: translate(-50%, -50%) rotate(45deg) scale(1.2);
    }
    100% {
      opacity: 0;
      transform: translate(-50%, -50%) rotate(45deg) scale(1);
    }
  }

  @keyframes hitmarkerFadeNeg45 {
    0% {
      opacity: 1;
      transform: translate(-50%, -50%) rotate(-45deg) scale(0.5);
    }
    20% {
      opacity: 1;
      transform: translate(-50%, -50%) rotate(-45deg) scale(1.2);
    }
    100% {
      opacity: 0;
      transform: translate(-50%, -50%) rotate(-45deg) scale(1);
    }
  }

  .loading-container {
    position: relative;
    width: 100%;
    height: 100%;
    min-height: 300px;
    background: linear-gradient(
      135deg,
      hsl(var(--fw-surface-deep, 235 21% 11%)) 0%,
      hsl(var(--fw-surface, 233 23% 17%)) 25%,
      hsl(var(--fw-surface-deep, 235 21% 11%)) 50%,
      hsl(var(--fw-surface, 233 23% 17%)) 75%,
      hsl(var(--fw-surface-deep, 235 21% 11%)) 100%
    );
    background-size: 400% 400%;
    animation: gradientShift 16s ease-in-out infinite;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    overflow: hidden;
    border-radius: 0;
    user-select: none;
    -webkit-user-select: none;
  }

  .bubble {
    position: absolute;
    border-radius: 50%;
    transition: opacity 1s ease-in-out;
    pointer-events: none;
    user-select: none;
  }

  .particle {
    position: absolute;
    border-radius: 50%;
    opacity: 0;
    animation: floatUp linear infinite;
    pointer-events: none;
    user-select: none;
  }

  .particle:nth-child(8n + 1) {
    background: hsl(var(--fw-accent, 218 79% 73%));
  }
  .particle:nth-child(8n + 2) {
    background: hsl(var(--fw-accent-secondary, 268 75% 76%));
  }
  .particle:nth-child(8n + 3) {
    background: hsl(var(--fw-success, 97 52% 51%));
  }
  .particle:nth-child(8n + 4) {
    background: hsl(var(--fw-info, 197 95% 74%));
  }
  .particle:nth-child(8n + 5) {
    background: hsl(var(--fw-danger, 352 86% 71%));
  }
  .particle:nth-child(8n + 6) {
    background: hsl(var(--fw-warning, 33 81% 64%));
  }
  .particle:nth-child(8n + 7) {
    background: hsl(var(--fw-accent, 218 79% 73%) / 0.8);
  }
  .particle:nth-child(8n + 8) {
    background: hsl(var(--fw-accent-secondary, 268 75% 76%) / 0.8);
  }

  .bubble:nth-child(8n + 1) {
    background: hsl(var(--fw-accent, 218 79% 73%) / 0.2);
  }
  .bubble:nth-child(8n + 2) {
    background: hsl(var(--fw-accent-secondary, 268 75% 76%) / 0.2);
  }
  .bubble:nth-child(8n + 3) {
    background: hsl(var(--fw-success, 97 52% 51%) / 0.2);
  }
  .bubble:nth-child(8n + 4) {
    background: hsl(var(--fw-info, 197 95% 74%) / 0.2);
  }
  .bubble:nth-child(8n + 5) {
    background: hsl(var(--fw-danger, 352 86% 71%) / 0.2);
  }
  .bubble:nth-child(8n + 6) {
    background: hsl(var(--fw-warning, 33 81% 64%) / 0.2);
  }
  .bubble:nth-child(8n + 7) {
    background: hsl(var(--fw-accent, 218 79% 73%) / 0.15);
  }
  .bubble:nth-child(8n + 8) {
    background: hsl(var(--fw-accent-secondary, 268 75% 76%) / 0.15);
  }

  .center-logo {
    position: absolute;
    top: 50%;
    left: 50%;
    display: flex;
    align-items: center;
    justify-content: center;
    z-index: 10;
    transition: transform 0.3s ease-out;
    user-select: none;
  }

  .logo-pulse {
    position: absolute;
    border-radius: 50%;
    background: hsl(var(--fw-accent, 218 79% 73%) / 0.15);
    animation: logoPulse 3s ease-in-out infinite;
    user-select: none;
    pointer-events: none;
    transition: transform 0.3s ease-out;
  }

  .logo-pulse.hovered {
    animation: logoPulse 1s ease-in-out infinite;
    transform: scale(1.2);
  }

  .logo-button {
    all: unset;
    cursor: pointer;
    display: block;
  }

  .logo-image {
    position: relative;
    z-index: 1;
    filter: drop-shadow(0 4px 8px hsl(var(--fw-surface-deep, 235 21% 11%) / 0.3));
    transition: all 0.3s ease-out;
    user-select: none;
    -webkit-user-drag: none;
    -webkit-touch-callout: none;
  }

  .logo-image.hovered {
    filter: drop-shadow(0 6px 12px hsl(var(--fw-surface-deep, 235 21% 11%) / 0.4)) brightness(1.1);
    transform: scale(1.1);
    cursor: pointer;
  }

  .message {
    position: absolute;
    bottom: 20%;
    left: 50%;
    transform: translateX(-50%);
    color: hsl(var(--fw-text-muted, 227 24% 74%));
    font-size: 16px;
    font-weight: 500;
    text-align: center;
    animation: fadeInOut 2s ease-in-out infinite;
    text-shadow: 0 2px 4px hsl(var(--fw-surface-deep, 235 21% 11%) / 0.5);
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    user-select: none;
    pointer-events: none;
  }

  .overlay-texture {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    background:
      radial-gradient(
        circle at 20% 80%,
        hsl(var(--fw-accent, 218 79% 73%) / 0.03) 0%,
        transparent 50%
      ),
      radial-gradient(
        circle at 80% 20%,
        hsl(var(--fw-accent-secondary, 268 75% 76%) / 0.03) 0%,
        transparent 50%
      ),
      radial-gradient(
        circle at 40% 40%,
        hsl(var(--fw-success, 97 52% 51%) / 0.02) 0%,
        transparent 50%
      );
    pointer-events: none;
    user-select: none;
  }

  .hitmarker {
    position: absolute;
    transform: translate(-50%, -50%);
    pointer-events: none;
    z-index: 100;
    width: 40px;
    height: 40px;
  }

  .hitmarker-line {
    position: absolute;
    width: 12px;
    height: 3px;
    background-color: hsl(var(--fw-text-bright, 220 13% 91%));
    box-shadow: 0 0 8px hsl(var(--fw-text-bright, 220 13% 91%) / 0.8);
    border-radius: 1px;
  }

  .hitmarker-line.tl {
    top: 25%;
    left: 25%;
    animation: hitmarkerFade45 0.6s ease-out forwards;
  }

  .hitmarker-line.tr {
    top: 25%;
    left: 75%;
    animation: hitmarkerFadeNeg45 0.6s ease-out forwards;
  }

  .hitmarker-line.bl {
    top: 75%;
    left: 25%;
    animation: hitmarkerFadeNeg45 0.6s ease-out forwards;
  }

  .hitmarker-line.br {
    top: 75%;
    left: 75%;
    animation: hitmarkerFade45 0.6s ease-out forwards;
  }
</style>
