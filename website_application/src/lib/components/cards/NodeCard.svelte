<script lang="ts">
	import { cn } from "$lib/utils";
	import { Badge } from "$lib/components/ui/badge";

	// Local interface matching Houdini schema
	interface NodeCardData {
		id: string;
		nodeName: string;
		region?: string | null;
		nodeType?: string | null;
		externalIp?: string | null;
		latitude?: number | null;
		longitude?: number | null;
		lastHeartbeat?: string | null;
		liveState?: { isHealthy?: boolean | null } | null;
	}

	let {
		node,
		class: className,
		...restProps
	}: {
		node: NodeCardData;
		class?: string;
		[key: string]: unknown;
	} = $props();

	// Derive status from liveState or fall back to "UNKNOWN"
	const status = $derived(
		node.liveState?.isHealthy === true ? "HEALTHY" :
		node.liveState?.isHealthy === false ? "UNHEALTHY" : "UNKNOWN"
	);
	const isHealthy = $derived(status === "HEALTHY");
	const statusTone = $derived(isHealthy ? "green" : status === "UNKNOWN" ? "neutral" : "red");
</script>

<div class={cn("slab slab--compact", className)} {...restProps}>
	<div class="slab-header">
		<div class="flex items-center justify-between w-full">
			<h3 class="font-semibold text-foreground">{node.nodeName}</h3>
			<Badge tone={statusTone} class="text-xs">
				{status}
			</Badge>
		</div>
	</div>

	<div class="slab-body--padded">
		<div class="space-y-2 text-sm">
			<div class="flex justify-between">
				<span class="text-muted-foreground">Region:</span>
				<span class="text-foreground">{node.region}</span>
			</div>
			<div class="flex justify-between">
				<span class="text-muted-foreground">Type:</span>
				<span class="text-foreground">{node.nodeType}</span>
			</div>
			{#if node.externalIp}
				<div class="flex justify-between">
					<span class="text-muted-foreground">IP:</span>
					<span class="text-foreground font-mono text-xs">{node.externalIp}</span>
				</div>
			{/if}
			{#if node.latitude && node.longitude}
				<div class="flex justify-between">
					<span class="text-muted-foreground">Coordinates:</span>
					<span class="text-foreground font-mono text-xs"
						>{node.latitude.toFixed(2)}, {node.longitude.toFixed(2)}</span
					>
				</div>
			{/if}
			{#if node.lastHeartbeat}
				<div class="flex justify-between">
					<span class="text-muted-foreground">Last Seen:</span>
					<span class="text-foreground text-xs"
						>{new Date(node.lastHeartbeat).toLocaleString()}</span
					>
				</div>
			{/if}
		</div>
	</div>
</div>
