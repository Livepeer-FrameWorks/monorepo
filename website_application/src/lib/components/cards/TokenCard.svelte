<script lang="ts">
	import { Badge } from "$lib/components/ui/badge";
	import { Button } from "$lib/components/ui/button";

	// Local interface matching Houdini schema
	interface DeveloperToken {
		id: string;
		tokenName: string;
		status: string;
		permissions: string | string[];
		lastUsedAt?: string | null;
		expiresAt?: string | null;
		createdAt: string | null;
	}

	let {
		token,
		onRevoke,
	}: {
		token: DeveloperToken;
		onRevoke?: () => void;
	} = $props();

	function formatDate(dateString: string | Date | null | undefined) {
		if (!dateString) return "Never";
		return new Date(dateString).toLocaleDateString();
	}

	function getStatusTone(status: string) {
		switch (status.toLowerCase()) {
			case "active":
				return "green";
			case "revoked":
				return "red";
			case "expired":
				return "orange";
			default:
				return "neutral";
		}
	}

	const statusTone = $derived(getStatusTone(token.status));
	const isActive = $derived(token.status.toLowerCase() === "active");
</script>

<div class="px-4 py-3 hover:bg-muted/30 transition-colors">
	<div class="flex items-start justify-between gap-2 mb-1">
		<span class="font-medium text-foreground text-sm truncate">{token.tokenName}</span>
		<Badge tone={statusTone} class="text-xs shrink-0">{token.status}</Badge>
	</div>
	<div class="flex items-center justify-between text-xs text-muted-foreground">
		<span>Last used: {formatDate(token.lastUsedAt)}</span>
		{#if isActive && onRevoke}
			<button
				class="text-destructive hover:underline"
				onclick={onRevoke}
			>
				Revoke
			</button>
		{/if}
	</div>
</div>
