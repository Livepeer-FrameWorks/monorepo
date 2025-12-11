<script lang="ts" module>
	import { type VariantProps, tv } from "tailwind-variants";

	export const badgeVariants = tv({
		base: "focus-visible:border-ring focus-visible:ring-ring/50 aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive inline-flex w-fit shrink-0 items-center justify-center gap-1 overflow-hidden whitespace-nowrap rounded-md border px-2 py-0.5 text-xs font-medium transition-[color,box-shadow] focus-visible:ring-[3px] [&>svg]:pointer-events-none [&>svg]:size-3",
		variants: {
			variant: {
				default:
					"bg-primary text-primary-foreground [a&]:hover:bg-primary/90 border-transparent",
				secondary:
					"relative isolate bg-[linear-gradient(135deg,color-mix(in_srgb,hsl(var(--primary))_82%,hsl(var(--brand-surface))_18%),color-mix(in_srgb,hsl(var(--accent))_76%,hsl(var(--brand-surface))_24%))] text-[hsl(var(--primary-foreground))] border-[hsl(var(--accent)/0.45)] shadow-[0_10px_22px_rgba(34,56,106,0.24),0_0_0_1px_hsl(var(--accent)/0.22)]",
				destructive:
					"bg-destructive [a&]:hover:bg-destructive/90 focus-visible:ring-destructive/20 dark:focus-visible:ring-destructive/40 dark:bg-destructive/70 border-transparent text-primary-foreground",
				outline: "text-foreground [a&]:hover:bg-accent/90 [a&]:hover:text-accent-foreground",
			},
			tone: {
				default: "",
				blue: "relative isolate bg-[linear-gradient(135deg,hsl(202_100%_78%),hsl(218_92%_52%))] text-[rgba(8,10,18,0.94)] border-[hsla(218,92%,32%,0.75)] shadow-[0_14px_28px_rgba(6,15,65,0.18)]",
				purple: "relative isolate bg-[linear-gradient(135deg,hsl(262_68%_72%),hsl(258_72%_48%))] text-[rgba(8,10,18,0.94)] border-[hsla(258,72%,32%,0.75)] shadow-[0_14px_28px_rgba(6,15,65,0.18)]",
				green: "relative isolate bg-[linear-gradient(135deg,hsl(96_55%_66%),hsl(134_52%_44%))] text-[rgba(8,10,18,0.94)] border-[hsla(134,52%,28%,0.75)] shadow-[0_14px_28px_rgba(6,15,65,0.18)]",
				yellow: "relative isolate bg-[linear-gradient(135deg,hsl(38_80%_68%),hsl(32_78%_46%))] text-[rgba(8,10,18,0.94)] border-[hsla(32,78%,30%,0.75)] shadow-[0_14px_28px_rgba(6,15,65,0.18)]",
				cyan: "relative isolate bg-[linear-gradient(135deg,hsl(196_100%_78%),hsl(201_92%_52%))] text-[rgba(8,10,18,0.94)] border-[hsla(201,92%,32%,0.75)] shadow-[0_14px_28px_rgba(6,15,65,0.18)]",
				orange: "relative isolate bg-[linear-gradient(135deg,hsl(28_92%_68%),hsl(20_88%_46%))] text-[rgba(8,10,18,0.94)] border-[hsla(20,88%,30%,0.75)] shadow-[0_14px_28px_rgba(6,15,65,0.18)]",
				red: "relative isolate bg-[linear-gradient(135deg,hsl(349_89%_72%),hsl(0_78%_46%))] text-[rgba(8,10,18,0.94)] border-[hsla(0,78%,30%,0.75)] shadow-[0_14px_28px_rgba(6,15,65,0.18)]",
				neutral: "relative isolate bg-[linear-gradient(135deg,color-mix(in_srgb,hsl(var(--brand-surface-strong))_90%,transparent),color-mix(in_srgb,hsl(var(--brand-surface))_85%,transparent))] text-[hsl(var(--brand-terminal))] border-[hsl(var(--border)/0.55)] shadow-[0_14px_28px_rgba(6,15,65,0.18)]",
			},
		},
		defaultVariants: {
			variant: "default",
			tone: "default",
		},
	});

	export type BadgeVariant = VariantProps<typeof badgeVariants>["variant"];
	export type BadgeTone = VariantProps<typeof badgeVariants>["tone"];
</script>

<script lang="ts">
	import type { HTMLAnchorAttributes } from "svelte/elements";
	import { cn, type WithElementRef } from "$lib/utils";

	let {
		ref = $bindable(null),
		href,
		class: className,
		variant = "default",
		tone = "default",
		children,
		...restProps
	}: WithElementRef<HTMLAnchorAttributes> & {
		variant?: BadgeVariant;
		tone?: BadgeTone;
	} = $props();
</script>

<svelte:element
	this={href ? "a" : "span"}
	bind:this={ref}
	data-slot="badge"
	{href}
	class={cn(badgeVariants({ variant, tone }), className)}
	{...restProps}
>
	{@render children?.()}
</svelte:element>
