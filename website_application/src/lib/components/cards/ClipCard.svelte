<script lang="ts">
	import { cn } from "$lib/utils";
	import { Badge } from "$lib/components/ui/badge";
	import { Button } from "$lib/components/ui/button";
	import { getIconComponent } from "$lib/iconUtils";

	// Clip type matching Houdini schema
	interface ClipData {
		id: string;
		clipHash?: string | null;
		title?: string | null;
		description?: string | null;
		startTime?: number | null;
		endTime?: number | null;
		duration?: number | null;
		status?: string | null;
		playbackId?: string | null;
		createdAt?: string | null;
	}

	let {
		clip,
		streamName,
		onPlay,
		class: className,
		...restProps
	}: {
		clip: ClipData;
		streamName: string;
		onPlay?: () => void;
		class?: string;
		[key: string]: unknown;
	} = $props();

	function formatDuration(seconds: number | null | undefined) {
		if (seconds == null) return "0:00";
		const minutes = Math.floor(seconds / 60);
		const remainingSeconds = seconds % 60;
		return `${minutes}:${remainingSeconds.toString().padStart(2, "0")}`;
	}

	function formatDate(dateString: string | Date) {
		return new Date(dateString).toLocaleDateString();
	}

	const statusTone = $derived(
		clip.status === "Available" || clip.status === "completed"
			? "green"
			: clip.status === "Processing" || clip.status === "processing"
				? "yellow"
				: clip.status === "Failed" || clip.status === "failed"
					? "red"
					: "blue"
	);

	const PlayIcon = getIconComponent("Play");
</script>

<div class={cn("slab slab--compact", className)} {...restProps}>
	<div class="slab-header">
		<div class="flex items-center justify-between w-full gap-2">
			<div class="min-w-0">
				<h3 class="font-semibold text-foreground truncate">{clip.title}</h3>
				<p class="text-xs text-muted-foreground">From: {streamName}</p>
			</div>
			<Badge tone={statusTone} class="text-xs shrink-0">
				{clip.status}
			</Badge>
		</div>
	</div>

	<div class="slab-body--padded">
		{#if clip.description}
			<p class="text-sm text-muted-foreground mb-3 line-clamp-2">
				{clip.description}
			</p>
		{/if}

		<div class="space-y-2 text-sm">
			<div class="flex justify-between">
				<span class="text-muted-foreground">Duration</span>
				<span class="text-foreground">{formatDuration(clip.duration)}</span>
			</div>
			<div class="flex justify-between">
				<span class="text-muted-foreground">Start Time</span>
				<span class="text-foreground">{formatDuration(clip.startTime)}</span>
			</div>
			<div class="flex justify-between">
				<span class="text-muted-foreground">Created</span>
				<span class="text-foreground">{clip.createdAt ? formatDate(clip.createdAt) : "N/A"}</span>
			</div>
		</div>
	</div>

	{#if onPlay}
		<div class="slab-actions">
			<Button
				variant="ghost"
				class="gap-2 justify-center"
				onclick={onPlay}
			>
				{#if PlayIcon}
					<PlayIcon class="size-4" />
				{/if}
				<span>Play Clip</span>
			</Button>
		</div>
	{/if}
</div>

<style>
	.line-clamp-2 {
		display: -webkit-box;
		-webkit-line-clamp: 2;
		-webkit-box-orient: vertical;
		line-clamp: 2;
		overflow: hidden;
	}
</style>
