<script lang="ts">
  import { page } from "$app/stores";
  import { Button } from "$lib/components/ui/button";
</script>

<div class="flex min-h-[60vh] items-center justify-center p-4">
  <div class="bg-card border border-border p-6 max-w-lg w-full shadow-2xl">
    <div class="flex items-center space-x-3 mb-4">
      <div class="flex-shrink-0">
        <svg class="w-8 h-8 text-destructive" fill="currentColor" viewBox="0 0 20 20">
          <path
            fill-rule="evenodd"
            d="M8.257 3.099c.765-1.36 2.722-1.36 3.486 0l5.58 9.92c.75 1.334-.213 2.98-1.742 2.98H4.42c-1.53 0-2.493-1.646-1.743-2.98l5.58-9.92zM11 13a1 1 0 11-2 0 1 1 0 012 0zm-1-8a1 1 0 00-1 1v3a1 1 0 002 0V6a1 1 0 00-1-1z"
            clip-rule="evenodd"
          />
        </svg>
      </div>
      <div>
        <h2 class="text-xl font-semibold text-destructive mb-1">
          {$page.status}
        </h2>
        <p class="text-muted-foreground text-sm">
          {$page.error?.message ?? "An unexpected error occurred"}
        </p>
      </div>
    </div>

    <div class="flex justify-end space-x-3">
      <Button variant="outline" onclick={() => history.back()}>Go Back</Button>
      <Button onclick={() => location.reload()}>Reload</Button>
    </div>
  </div>
</div>
