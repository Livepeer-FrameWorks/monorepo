<script lang="ts">
  import { resolve } from "$app/paths";
  import { Key, ShieldCheck } from "lucide-svelte";

  interface Props {
    active: "api-keys" | "signing-keys";
  }

  let { active }: Props = $props();

  const tabs = [
    {
      id: "api-keys",
      href: "/developer/api-keys",
      label: "API Keys",
      icon: Key,
      description: "Programmatic API access",
    },
    {
      id: "signing-keys",
      href: "/developer/signing-keys",
      label: "Signing Keys",
      icon: ShieldCheck,
      description: "Playback JWT signing",
    },
  ] as const;
</script>

<div class="border-b border-border bg-background">
  <div class="px-4 sm:px-6 lg:px-8">
    <div class="flex flex-wrap gap-2 py-2">
      {#each tabs as tab (tab.id)}
        {@const Icon = tab.icon}
        <a
          href={resolve(tab.href)}
          aria-current={active === tab.id ? "page" : undefined}
          class="group flex min-w-0 items-center gap-2 border px-3 py-2 text-sm transition-colors {active ===
          tab.id
            ? 'border-primary bg-primary/10 text-foreground'
            : 'border-border text-muted-foreground hover:border-primary/50 hover:text-foreground'}"
        >
          <Icon class="h-4 w-4 shrink-0" />
          <span class="font-medium">{tab.label}</span>
          <span class="hidden text-xs text-muted-foreground md:inline">{tab.description}</span>
        </a>
      {/each}
    </div>
  </div>
</div>
