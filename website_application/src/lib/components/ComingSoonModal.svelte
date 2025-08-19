<script>
  import { createEventDispatcher } from "svelte";
  import { getIconComponent } from "../iconUtils.js";

  /** @type {boolean} */
  export let show = false;

  /** @type {any} */
  export let item = null;

  const dispatch = createEventDispatcher();

  function close() {
    show = false;
    dispatch("close");
  }

  // Escape key handler
  /**
   * @param {KeyboardEvent} event
   */
  function handleKeydown(event) {
    if (event.key === "Escape") {
      close();
    }
  }
</script>

<svelte:window on:keydown={handleKeydown} />

{#if show && item}
  <!-- Modal Backdrop -->
  <!-- svelte-ignore a11y-click-events-have-key-events -->
  <!-- svelte-ignore a11y-no-noninteractive-element-interactions -->
  <div
    class="fixed inset-0 bg-black/50 backdrop-blur-sm z-50 flex items-center justify-center p-4"
    on:click={close}
    role="dialog"
    aria-modal="true"
    aria-labelledby="modal-title"
  >
    <!-- Modal Content -->
    <!-- svelte-ignore a11y-click-events-have-key-events -->
    <!-- svelte-ignore a11y-no-noninteractive-element-interactions -->
    <div
      class="glow-card max-w-md w-full p-6 relative"
      role="document"
      on:click|stopPropagation
    >
      <!-- Close Button -->
      <button
        on:click={close}
        class="absolute top-4 right-4 text-tokyo-night-comment hover:text-tokyo-night-fg transition-colors duration-200"
        aria-label="Close modal"
      >
        <svg
          class="w-6 h-6"
          fill="none"
          stroke="currentColor"
          viewBox="0 0 24 24"
        >
          <path
            stroke-linecap="round"
            stroke-linejoin="round"
            stroke-width="2"
            d="M6 18L18 6M6 6l12 12"
          />
        </svg>
      </button>

      <!-- Feature Icon and Title -->
      <div class="flex items-center space-x-3 mb-4">
        <div class="w-10 h-10 flex items-center justify-center bg-tokyo-night-bg-highlight rounded-lg">
          <svelte:component 
            this={getIconComponent(item.icon)} 
            class="w-6 h-6 text-tokyo-night-fg" 
          />
        </div>
        <div>
          <h2
            id="modal-title"
            class="text-xl font-semibold text-tokyo-night-fg"
          >
            {item.name}
          </h2>
          <span
            class="inline-flex items-center px-2 py-1 rounded-full text-xs font-medium bg-tokyo-night-yellow/20 text-tokyo-night-yellow"
          >
            Coming Soon
          </span>
        </div>
      </div>

      <!-- Description -->
      <p class="text-tokyo-night-fg-dark mb-6">
        {item.description ||
          "This feature is planned for future release."}
      </p>

      <!-- Status -->
      <div
        class="mb-6 p-4 bg-tokyo-night-yellow/10 border border-tokyo-night-yellow/30 rounded-lg"
      >
        <div class="flex items-center space-x-2 mb-2">
          <svg
            class="w-5 h-5 text-tokyo-night-yellow"
            fill="none"
            stroke="currentColor"
            viewBox="0 0 24 24"
          >
            <path
              stroke-linecap="round"
              stroke-linejoin="round"
              stroke-width="2"
              d="M12 8v4m0 4h.01M21 12a9 9 0 11-18 0 9 9 0 0118 0z"
            />
          </svg>
          <span class="text-tokyo-night-yellow font-medium text-sm"
            >In Development</span
          >
        </div>
        <p class="text-tokyo-night-yellow/80 text-sm">
          This feature is part of our development roadmap. Check our documentation for current implementation status and timeline.
        </p>
      </div>

      <!-- Feature Benefits -->
      <div class="mb-6">
        <h3 class="text-sm font-medium text-tokyo-night-fg mb-3">
          What to expect:
        </h3>
        <ul class="space-y-2 text-sm text-tokyo-night-fg-dark">
          {#if item.name === "Stream Settings"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>Configure transcoding, recording, and stream options</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>Set stream access controls and metadata</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>Customize stream thumbnails and descriptions</span>
            </li>
          {:else if item.name === "Stream Composer"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-blue mt-0.5">•</span>
              <span>Multi-stream compositing with picture-in-picture</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-blue mt-0.5">•</span>
              <span>Visual editor for stream layouts and overlays</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-blue mt-0.5">•</span>
              <span>Combine multiple input streams into one output</span>
            </li>
          {:else if item.name === "Browser Streaming"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-purple mt-0.5">•</span>
              <span>Stream directly from browser without OBS</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-purple mt-0.5">•</span>
              <span>WebRTC-based ultra-low latency streaming</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-purple mt-0.5">•</span>
              <span>One-click "Go Live" from your dashboard</span>
            </li>
          {:else if item.name === "Clips"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>Create clips from live streams with timeline editor</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>Requires storage node routing and API integration</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>MistServer supports clipping capability</span>
            </li>
          {:else if item.name === "Recordings"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>Live stream recording and archival</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>Requires storage node routing and metering work</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>MistServer recording capability exists</span>
            </li>
          {:else if item.name === "Reports"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Generate detailed analytics reports</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Export data to CSV/PDF formats</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Historical data analysis and trends</span>
            </li>
          {:else if item.name === "Audience Insights"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Understand viewer demographics and behavior</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Engagement metrics and retention analysis</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Audience segmentation and insights</span>
            </li>
          {:else if item.name === "Node Management"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>Manage self-hosted Edge nodes</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>Monitor node health and performance</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>Remote configuration and updates</span>
            </li>
          {:else if item.name === "Device Discovery"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Auto-discover AV devices and cameras</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Feature support in testing, requires deployment pipeline</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Integrated sidecar for remote management</span>
            </li>
          {:else if item.name === "Network Status"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Monitor network health and performance</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Track bandwidth usage and latency</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Network troubleshooting and diagnostics</span>
            </li>
          {:else if item.name === "AI Processing"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-purple mt-0.5">•</span>
              <span>Real-time AI analysis and processing</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-purple mt-0.5">•</span>
              <span>Requires metering, devops work, and infrastructure scaling</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-purple mt-0.5">•</span>
              <span>Edge node features currently in testing</span>
            </li>
          {:else if item.name === "Live Transcription"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-purple mt-0.5">•</span>
              <span>Real-time speech-to-text with live captions</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-purple mt-0.5">•</span>
              <span>Multiple language support</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-purple mt-0.5">•</span>
              <span>Part of AI processing pipeline</span>
            </li>
          {:else if item.name === "Content Moderation"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-purple mt-0.5">•</span>
              <span>AI-powered content filtering and safety</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-purple mt-0.5">•</span>
              <span>Automated content classification</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-purple mt-0.5">•</span>
              <span>Not yet implemented</span>
            </li>
          {:else if item.name === "Auto Clipping"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-purple mt-0.5">•</span>
              <span>AI-powered highlight detection</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-purple mt-0.5">•</span>
              <span>Automated clip generation from streams</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-purple mt-0.5">•</span>
              <span>Part of AI processing features</span>
            </li>
          {:else if item.name === "Profile Settings"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-yellow mt-0.5">•</span>
              <span>Account profile management and preferences</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-yellow mt-0.5">•</span>
              <span>User settings and configuration</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-yellow mt-0.5">•</span>
              <span>Personal information management</span>
            </li>
          {:else if item.name === "Security"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-yellow mt-0.5">•</span>
              <span>Security settings and access control</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-yellow mt-0.5">•</span>
              <span>Two-factor authentication setup</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-yellow mt-0.5">•</span>
              <span>Session management and audit logs</span>
            </li>
          {:else if item.name === "Notifications"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-yellow mt-0.5">•</span>
              <span>Configure alerts and notification preferences</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-yellow mt-0.5">•</span>
              <span>Stream alerts and usage notifications</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-yellow mt-0.5">•</span>
              <span>Email and webhook notification settings</span>
            </li>
          {:else if item.name === "Team Members"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-blue mt-0.5">•</span>
              <span>Invite and manage team members</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-blue mt-0.5">•</span>
              <span>Database models exist, no API or UI yet</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-blue mt-0.5">•</span>
              <span>Role-based access control planned</span>
            </li>
          {:else if item.name === "Permissions"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-blue mt-0.5">•</span>
              <span>Configure role-based access control</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-blue mt-0.5">•</span>
              <span>Team member permission management</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-blue mt-0.5">•</span>
              <span>Stream and analytics access controls</span>
            </li>
          {:else if item.name === "Team Activity"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-blue mt-0.5">•</span>
              <span>View team member activity and logs</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-blue mt-0.5">•</span>
              <span>Team workflow tracking</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-blue mt-0.5">•</span>
              <span>Audit trails and activity monitoring</span>
            </li>
          {:else if item.name === "Webhooks"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>Configure event notifications and integrations</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>Needs scoping and exploratory work</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>Stream lifecycle event notifications</span>
            </li>
          {:else if item.name === "SDKs & Libraries"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>NPM packages and web player components</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>JavaScript SDK for easy integration</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>Not yet implemented</span>
            </li>
          {:else if item.name === "API Playground"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>Test API endpoints interactively</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>Interactive API explorer with examples</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-green mt-0.5">•</span>
              <span>Authentication and rate limiting testing</span>
            </li>
          {:else if item.name === "Help Center"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Browse documentation and tutorials</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Setup guides and troubleshooting</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Will be another microservice - simple and in-house</span>
            </li>
          {:else if item.name === "Support Tickets"}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Get help from our support team</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Ticket tracking and management</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-cyan mt-0.5">•</span>
              <span>Part of planned support microservice</span>
            </li>
          {:else}
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-blue mt-0.5">•</span>
              <span>Feature-specific functionality for your streaming needs</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-blue mt-0.5">•</span>
              <span>Professional-grade tools with enterprise reliability</span>
            </li>
            <li class="flex items-start space-x-2">
              <span class="text-tokyo-night-blue mt-0.5">•</span>
              <span>Check our roadmap for current implementation status</span>
            </li>
          {/if}
        </ul>
      </div>

      <!-- Actions -->
      <div class="flex space-x-3">
        <button on:click={close} class="btn-secondary flex-1"> Got it </button>
        <button
          on:click={() =>
            window.open("https://frameworks.network/contact", "_blank")}
          class="btn-primary flex-1"
        >
          Contact Us
        </button>
      </div>
    </div>
  </div>
{/if}

