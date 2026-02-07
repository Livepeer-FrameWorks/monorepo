<script lang="ts">
  import { onMount } from "svelte";
  import { Turnstile } from "svelte-turnstile";
  import type { BotProtectionData } from "../../lib/auth";

  type LoginHandler = (
    email: string,
    password: string,
    botProtection: BotProtectionData
  ) => Promise<string | null>;
  type RegisterHandler = (
    email: string,
    password: string,
    displayName: string,
    botProtection: BotProtectionData
  ) => Promise<string | null>;

  interface Props {
    onLogin: LoginHandler;
    onRegister: RegisterHandler;
  }

  let { onLogin, onRegister }: Props = $props();

  let mode = $state<"login" | "register">("login");
  let email = $state("");
  let password = $state("");
  let confirmPassword = $state("");
  let displayName = $state("");
  let error = $state("");
  let submitting = $state(false);

  // Bot protection
  const turnstileSiteKey = import.meta.env.PUBLIC_TURNSTILE_AUTH_SITE_KEY || "";
  const defaultHumanCheck = turnstileSiteKey ? "robot" : "human";

  let phoneNumber = $state("");
  let humanCheck = $state(defaultHumanCheck);
  let turnstileToken = $state("");
  let turnstileWidgetId = $state("");
  let formShownAt = 0;
  let interactions = { mouse: false, typed: false, focused: false };

  let formRef = $state<HTMLFormElement | null>(null);

  onMount(() => {
    formShownAt = Date.now();
  });

  function trackMouse() {
    interactions.mouse = true;
  }
  function trackTyping() {
    interactions.typed = true;
  }
  function trackFocus() {
    interactions.focused = true;
  }

  function buildBotProtection(): BotProtectionData {
    return {
      turnstileToken: turnstileToken || undefined,
      phone_number: phoneNumber || undefined,
      human_check: humanCheck,
      behavior: JSON.stringify({
        formShownAt,
        submittedAt: Date.now(),
        mouse: interactions.mouse,
        typed: interactions.typed,
        focused: interactions.focused,
      }),
    };
  }

  function resetTurnstile() {
    if (typeof window !== "undefined" && turnstileWidgetId) {
      try {
        (window as any)?.turnstile?.reset?.(turnstileWidgetId);
      } catch {}
    }
    turnstileToken = "";
    humanCheck = defaultHumanCheck;
  }

  async function handleSubmit(event: SubmitEvent) {
    event.preventDefault();
    if (submitting) return;

    error = "";

    if (turnstileSiteKey && !turnstileToken) {
      error = "Please complete the verification.";
      return;
    }

    if (mode === "register") {
      if (password !== confirmPassword) {
        error = "Passwords do not match.";
        return;
      }
      if (password.length < 8) {
        error = "Password must be at least 8 characters.";
        return;
      }
    }

    submitting = true;

    try {
      let result: string | null;
      const botProtection = buildBotProtection();
      if (mode === "login") {
        result = await onLogin(email, password, botProtection);
      } else {
        result = await onRegister(email, password, displayName, botProtection);
      }
      if (result) error = result;
    } finally {
      submitting = false;
      if (turnstileSiteKey) resetTurnstile();
    }
  }

  function switchMode() {
    mode = mode === "login" ? "register" : "login";
    error = "";
    confirmPassword = "";
    if (turnstileSiteKey) resetTurnstile();
  }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<form
  class="docs-auth-form"
  bind:this={formRef}
  onsubmit={handleSubmit}
  onmousemove={trackMouse}
  oninput={trackTyping}
  onfocusin={trackFocus}
>
  <div class="docs-auth-form__title">
    {mode === "login" ? "Sign in" : "Create account"}
  </div>

  {#if error}
    <div class="docs-auth-form__error">{error}</div>
  {/if}

  {#if mode === "register"}
    <label class="docs-auth-form__field">
      <span class="docs-auth-form__label">Name</span>
      <input
        type="text"
        class="docs-auth-form__input"
        bind:value={displayName}
        required
        autocomplete="name"
        placeholder="Display name"
      />
    </label>
  {/if}

  <label class="docs-auth-form__field">
    <span class="docs-auth-form__label">Email</span>
    <input
      type="email"
      class="docs-auth-form__input"
      bind:value={email}
      required
      autocomplete="email"
      placeholder="you@example.com"
    />
  </label>

  <label class="docs-auth-form__field">
    <span class="docs-auth-form__label">Password</span>
    <input
      type="password"
      class="docs-auth-form__input"
      bind:value={password}
      required
      autocomplete={mode === "login" ? "current-password" : "new-password"}
      placeholder={mode === "login" ? "Password" : "Min 8 characters"}
      minlength={mode === "register" ? 8 : undefined}
    />
  </label>

  {#if mode === "register"}
    <label class="docs-auth-form__field">
      <span class="docs-auth-form__label">Confirm password</span>
      <input
        type="password"
        class="docs-auth-form__input"
        bind:value={confirmPassword}
        required
        autocomplete="new-password"
        placeholder="Confirm your password"
        minlength={8}
      />
    </label>
  {/if}

  {#if !turnstileSiteKey}
    <!-- Honeypot field — hidden from users, visible to bots -->
    <div class="sr-only" aria-hidden="true">
      <label for="docs-phone">Phone (leave blank)</label>
      <input
        id="docs-phone"
        type="text"
        bind:value={phoneNumber}
        tabindex={-1}
        autocomplete="off"
      />
    </div>

    <!-- Fallback human verification -->
    <fieldset class="docs-auth-form__field docs-auth-form__verification">
      <legend class="docs-auth-form__label">Verification</legend>
      <label class="docs-auth-form__radio">
        <input type="radio" name="human_check" value="human" bind:group={humanCheck} />
        I am human
      </label>
      <label class="docs-auth-form__radio">
        <input type="radio" name="human_check" value="robot" bind:group={humanCheck} />
        I am a robot
      </label>
    </fieldset>
  {/if}

  {#if turnstileSiteKey}
    <div class="docs-auth-form__field docs-auth-form__turnstile">
      <span class="docs-auth-form__label">Verification</span>
      <Turnstile
        siteKey={turnstileSiteKey}
        theme="dark"
        action={mode === "login" ? "login_form" : "register_form"}
        bind:widgetId={turnstileWidgetId}
        on:callback={({ detail }) => {
          turnstileToken = detail?.token ?? detail ?? "";
          humanCheck = "human";
          error = "";
        }}
        on:error={() => resetTurnstile()}
        on:expire={() => resetTurnstile()}
      />
    </div>
  {/if}

  <button
    type="submit"
    class="docs-auth-form__submit"
    disabled={submitting ||
      (turnstileSiteKey && !turnstileToken) ||
      (!turnstileSiteKey && humanCheck !== "human")}
  >
    {#if submitting}
      <span class="docs-auth-spinner"></span>
    {:else}
      {mode === "login" ? "Sign in" : "Create account"}
    {/if}
  </button>

  <div class="docs-auth-form__switch">
    {mode === "login" ? "No account?" : "Already have an account?"}
    <button type="button" class="docs-auth-form__switch-btn" onclick={switchMode}>
      {mode === "login" ? "Register" : "Sign in"}
    </button>
  </div>
</form>
