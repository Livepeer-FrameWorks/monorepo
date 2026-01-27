<!--
  IdleScreen.svelte - Idle/offline state screen with Tokyo Night theme
  Port of src/components/IdleScreen.tsx

  Features:
  - Animated colored bubbles with Tokyo Night palette
  - Center logo with mouse-tracking "push away" effect
  - Bouncing DVD logo component
  - Hitmarker sound effects (Web Audio API synthesis)
  - Floating particles with gradients
  - Pulsing circle around logo
  - Animated background gradient shifts
  - Status overlay at bottom
-->
<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import type { StreamStatus } from "@livepeer-frameworks/player-core";
  import DvdLogo from "./DvdLogo.svelte";
  import logomarkAsset from "./assets/logomark.svg";

  interface Props {
    status?: StreamStatus;
    message?: string;
    percentage?: number;
    error?: string;
    onRetry?: () => void;
  }

  let {
    status = "OFFLINE",
    message = "Waiting for stream...",
    percentage = undefined,
    error = undefined,
    onRetry = undefined,
  }: Props = $props();

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

  // Tokyo Night inspired pastel colors for bubbles
  const bubbleColors = [
    "rgba(122, 162, 247, 0.2)", // Terminal Blue
    "rgba(187, 154, 247, 0.2)", // Terminal Magenta
    "rgba(158, 206, 106, 0.2)", // Strings/CSS classes
    "rgba(115, 218, 202, 0.2)", // Terminal Green
    "rgba(125, 207, 255, 0.2)", // Terminal Cyan
    "rgba(247, 118, 142, 0.2)", // Keywords/Terminal Red
    "rgba(224, 175, 104, 0.2)", // Terminal Yellow
    "rgba(42, 195, 222, 0.2)", // Language functions
  ];

  // Particle colors
  const particleColors = [
    "#7aa2f7", // Terminal Blue
    "#bb9af7", // Terminal Magenta
    "#9ece6a", // Strings/CSS classes
    "#73daca", // Terminal Green
    "#7dcfff", // Terminal Cyan
    "#f7768e", // Keywords/Terminal Red
    "#e0af68", // Terminal Yellow
    "#2ac3de", // Language functions
  ];

  // Generate random particles (matching React's 12 particles)
  const particles = Array.from({ length: 12 }, (_, i) => ({
    left: Math.random() * 100,
    size: Math.random() * 4 + 2,
    color: particleColors[i % 8],
    duration: 8 + Math.random() * 4,
    delay: Math.random() * 8,
  }));

  // Animated bubble state
  interface BubbleState {
    position: { top: number; left: number };
    size: number;
    opacity: number;
    color: string;
    timeoutId: ReturnType<typeof setTimeout> | null;
  }

  let bubbles = $state<BubbleState[]>(
    Array.from({ length: 8 }, (_, i) => ({
      position: { top: Math.random() * 80 + 10, left: Math.random() * 80 + 10 },
      size: Math.random() * 60 + 30,
      opacity: 0,
      color: bubbleColors[i % bubbleColors.length],
      timeoutId: null,
    }))
  );

  // Bubble animation cycle
  function animateBubble(index: number) {
    bubbles[index].opacity = 0.15;

    const visibleDuration = 4000 + Math.random() * 3000;

    const timeout1 = setTimeout(() => {
      bubbles[index].opacity = 0;

      const timeout2 = setTimeout(() => {
        bubbles[index].position = {
          top: Math.random() * 80 + 10,
          left: Math.random() * 80 + 10,
        };
        bubbles[index].size = Math.random() * 60 + 30;

        const timeout3 = setTimeout(() => {
          animateBubble(index);
        }, 200);
        bubbles[index].timeoutId = timeout3;
      }, 1500);
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
      const hitmarkerDataUrl =
        "data:audio/mpeg;base64,SUQzBAAAAAAANFRDT04AAAAHAAADT3RoZXIAVFNTRQAAAA8AAANMYXZmNTcuODMuMTAwAAAAAAAAAAAA" +
        "AAD/+1QAAAAAAAAAAAAAAAAAAAAA" +
        "AAAAAAAAAAAAAAAAAAAAAABJbmZvAAAADwAAAAYAAAnAADs7Ozs7Ozs7Ozs7Ozs7OztiYmJiYmJiYmJi" +
        "YmJiYmJiYomJiYmJiYmJiYmJiYmJiYmxsbGxsbGxsbGxsbGxsbGxsdjY2NjY2NjY2NjY2NjY2NjY////" +
        "/////////////////wAAAABMYXZjNTcuMTAAAAAAAAAAAAAAAAAkAkAAAAAAAAAJwOuMZun/+5RkAA8S" +
        "/F23AGAaAi0AF0AAAAAInXsEAIRXyQ8D4OQgjEhE3cO7ujuHF0XCOu4G7xKbi3Funu7u7p9dw7unu7u7" +
        "p7u7u6fXcW7om7u7uiU3dxdT67u7p7uHdxelN3cW6fXcW7oXXd3eJTd3d0+u4t3iXdw4up70W4uiPruL" +
        "DzMw8Pz79Y99JfkyfPv5/h9uTJoy79Y99Y97q3vyZPJk0ZfrL6x73Vn+J35dKKS/STQyQ8CAiCPNuRAO" +
        "OqquAx+fzJeBKDAsgAMBuWcBsHKhjJTcCwIALyAvABbI0ZIcCmP8jHJe8gZAdVRp2TpnU/kUXV4iQuBA";

      const audio = new Audio(hitmarkerDataUrl);
      audio.volume = 0.3;
      audio.play().catch(() => {
        createSyntheticHitmarkerSound();
      });
    } catch {
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

  // Status helpers
  function getStatusLabel(s?: StreamStatus): string {
    switch (s) {
      case "ONLINE":
        return "ONLINE";
      case "OFFLINE":
        return "OFFLINE";
      case "INITIALIZING":
        return "STARTING";
      case "BOOTING":
        return "STARTING";
      case "WAITING_FOR_DATA":
        return "WAITING";
      case "SHUTTING_DOWN":
        return "ENDING";
      case "ERROR":
        return "ERROR";
      case "INVALID":
        return "ERROR";
      default:
        return "CONNECTING";
    }
  }

  let _statusLabel = $derived(getStatusLabel(status));
  let showRetry = $derived((status === "ERROR" || status === "INVALID") && onRetry);
  let showProgress = $derived(status === "INITIALIZING" && percentage !== undefined);
  let displayMessage = $derived(error || message);
  let isLoading = $derived(
    status === "INITIALIZING" || status === "BOOTING" || status === "WAITING_FOR_DATA" || !status
  );
  let isError = $derived(status === "ERROR" || status === "INVALID");
  let isOffline = $derived(status === "OFFLINE");

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
  class="idle-container fw-player-root"
  role="status"
  aria-label="Stream status"
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
        background: {particle.color};
        animation-duration: {particle.duration}s;
        animation-delay: {particle.delay}s;
      "
    />
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
        background: {bubble.color};
        opacity: {bubble.opacity};
      "
    />
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
    />

    <!-- Logo image -->
    <img
      src={logomarkAsset}
      alt="Logo"
      class="logo-image"
      class:hovered={isHovered}
      style="width: {logoSize}px; height: {logoSize}px;"
      onclick={handleLogoClick}
      draggable="false"
    />
  </div>

  <!-- Bouncing DVD Logo -->
  <DvdLogo parentRef={containerRef} scale={0.08} />

  <!-- Status overlay at bottom -->
  <div class="status-overlay">
    <div class="status-indicator">
      <!-- Status icon -->
      {#if isLoading}
        <svg
          class="status-icon spinning"
          fill="none"
          viewBox="0 0 24 24"
          style="color: hsl(var(--tn-yellow, 40 95% 64%));"
        >
          <circle
            style="opacity: 0.25;"
            cx="12"
            cy="12"
            r="10"
            stroke="currentColor"
            stroke-width="4"
          />
          <path
            style="opacity: 0.75;"
            fill="currentColor"
            d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
          />
        </svg>
      {:else if isOffline}
        <svg
          class="status-icon"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
          style="color: hsl(var(--tn-red, 348 100% 72%));"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M18.364 5.636a9 9 0 010 12.728m0 0l-2.829-2.829m2.829 2.829L21 21M15.536 8.464a5 5 0 010 7.072m0 0l-2.829-2.829m-4.243 2.829a4.978 4.978 0 01-1.414-2.83m-1.414 5.658a9 9 0 01-2.167-9.238m7.824 2.167a1 1 0 111.414 1.414m-1.414-1.414L3 3m8.293 8.293l1.414 1.414"
          />
        </svg>
      {:else if isError}
        <svg
          class="status-icon"
          fill="none"
          viewBox="0 0 24 24"
          stroke="currentColor"
          style="color: hsl(var(--tn-red, 348 100% 72%));"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M12 9v2m0 4h.01m-6.938 4h13.856c1.54 0 2.502-1.667 1.732-3L13.732 4c-.77-1.333-2.694-1.333-3.464 0L3.34 16c-.77 1.333.192 3 1.732 3z"
          />
        </svg>
      {:else}
        <svg
          class="status-icon spinning"
          fill="none"
          viewBox="0 0 24 24"
          style="color: hsl(var(--tn-cyan, 193 100% 75%));"
        >
          <circle
            style="opacity: 0.25;"
            cx="12"
            cy="12"
            r="10"
            stroke="currentColor"
            stroke-width="4"
          />
          <path
            style="opacity: 0.75;"
            fill="currentColor"
            d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4zm2 5.291A7.962 7.962 0 014 12H0c0 3.042 1.135 5.824 3 7.938l3-2.647z"
          />
        </svg>
      {/if}
      <span>{displayMessage}</span>
    </div>

    <!-- Progress bar -->
    {#if showProgress}
      <div class="progress-bar">
        <div class="progress-fill" style="width: {Math.min(100, percentage ?? 0)}%;" />
      </div>
    {/if}

    <!-- Retry button -->
    {#if showRetry}
      <button type="button" class="retry-button" onclick={onRetry}> Retry </button>
    {/if}
  </div>

  <!-- Subtle overlay texture -->
  <div class="overlay-texture" />
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

  @keyframes spin {
    from {
      transform: rotate(0deg);
    }
    to {
      transform: rotate(360deg);
    }
  }

  .idle-container {
    position: absolute;
    inset: 0;
    z-index: 5;
    background: linear-gradient(
      135deg,
      hsl(var(--tn-bg-dark, 235 21% 11%)) 0%,
      hsl(var(--tn-bg, 233 23% 17%)) 25%,
      hsl(var(--tn-bg-dark, 235 21% 11%)) 50%,
      hsl(var(--tn-bg, 233 23% 17%)) 75%,
      hsl(var(--tn-bg-dark, 235 21% 11%)) 100%
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
    background: rgba(122, 162, 247, 0.15);
    animation: logoPulse 3s ease-in-out infinite;
    user-select: none;
    pointer-events: none;
    transition: transform 0.3s ease-out;
  }

  .logo-pulse.hovered {
    animation: logoPulse 1s ease-in-out infinite;
    transform: scale(1.2);
  }

  .logo-image {
    position: relative;
    z-index: 1;
    filter: drop-shadow(0 4px 8px rgba(36, 40, 59, 0.3));
    transition: all 0.3s ease-out;
    user-select: none;
    -webkit-user-drag: none;
    -webkit-touch-callout: none;
  }

  .logo-image.hovered {
    filter: drop-shadow(0 6px 12px rgba(36, 40, 59, 0.4)) brightness(1.1);
    transform: scale(1.1);
    cursor: pointer;
  }

  .overlay-texture {
    position: absolute;
    top: 0;
    left: 0;
    right: 0;
    bottom: 0;
    background:
      radial-gradient(circle at 20% 80%, rgba(122, 162, 247, 0.03) 0%, transparent 50%),
      radial-gradient(circle at 80% 20%, rgba(187, 154, 247, 0.03) 0%, transparent 50%),
      radial-gradient(circle at 40% 40%, rgba(158, 206, 106, 0.02) 0%, transparent 50%);
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
    background-color: #ffffff;
    box-shadow: 0 0 8px rgba(255, 255, 255, 0.8);
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

  .status-overlay {
    position: absolute;
    bottom: 16px;
    left: 50%;
    transform: translateX(-50%);
    z-index: 20;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 8px;
    max-width: 280px;
    text-align: center;
  }

  .status-indicator {
    display: flex;
    align-items: center;
    gap: 8px;
    color: #787c99;
    font-size: 13px;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  }

  .status-icon {
    width: 20px;
    height: 20px;
  }

  .status-icon.spinning {
    animation: spin 1s linear infinite;
  }

  .progress-bar {
    width: 160px;
    height: 4px;
    background: rgba(65, 72, 104, 0.4);
    border-radius: 2px;
    overflow: hidden;
  }

  .progress-fill {
    height: 100%;
    background: hsl(var(--tn-cyan, 193 100% 75%));
    transition: width 0.3s ease-out;
  }

  .retry-button {
    padding: 6px 16px;
    background: transparent;
    border: 1px solid rgba(122, 162, 247, 0.4);
    border-radius: 4px;
    color: #7aa2f7;
    font-size: 11px;
    font-weight: 500;
    cursor: pointer;
    transition: all 0.2s ease;
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
  }

  .retry-button:hover {
    background: rgba(122, 162, 247, 0.1);
  }
</style>
