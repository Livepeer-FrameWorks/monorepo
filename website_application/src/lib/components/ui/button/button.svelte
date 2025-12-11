<script lang="ts" module>
	import { cn, type WithElementRef } from "$lib/utils";
	import type { HTMLAnchorAttributes, HTMLButtonAttributes } from "svelte/elements";
	import { type VariantProps, tv } from "tailwind-variants";

	export const buttonVariants = tv({
		base: "cursor-pointer focus-visible:border-ring focus-visible:ring-ring/50 aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40 aria-invalid:border-destructive inline-flex shrink-0 items-center justify-center gap-2 whitespace-nowrap rounded-md text-sm font-medium outline-none transition-all duration-200 focus-visible:ring-[3px] disabled:pointer-events-none disabled:opacity-50 aria-disabled:pointer-events-none aria-disabled:opacity-50 [&_svg:not([class*='size-'])]:size-4 [&_svg]:pointer-events-none [&_svg]:shrink-0",
		variants: {
			variant: {
				default: "bg-primary text-primary-foreground shadow-[0_12px_28px_rgba(0,0,0,0.35)] hover:bg-primary/90 hover:-translate-y-0.5 hover:shadow-[0_24px_48px_rgba(0,0,0,0.45)]",
				destructive:
					"bg-destructive shadow-[0_12px_28px_rgba(0,0,0,0.35)] hover:bg-destructive/90 hover:-translate-y-0.5 hover:shadow-[0_24px_48px_rgba(0,0,0,0.45)] focus-visible:ring-destructive/20 dark:focus-visible:ring-destructive/40 dark:bg-destructive/60 text-primary-foreground",
				outline:
					"bg-background shadow-[0_12px_28px_rgba(0,0,0,0.35)] hover:bg-accent/90 hover:text-accent-foreground hover:-translate-y-0.5 hover:shadow-[0_24px_48px_rgba(0,0,0,0.45)] hover:border-accent dark:bg-input/30 dark:border-input dark:hover:bg-input/50 border",
				secondary: "bg-secondary text-secondary-foreground shadow-[0_12px_28px_rgba(0,0,0,0.35)] hover:bg-secondary/80 hover:-translate-y-0.5 hover:shadow-[0_24px_48px_rgba(0,0,0,0.45)]",
				ghost: "hover:bg-accent/90 hover:text-accent-foreground hover:-translate-y-0.5 dark:hover:bg-accent/50",
				link: "text-primary underline-offset-4 hover:underline",
				cta: "relative overflow-hidden border text-primary-foreground font-semibold uppercase tracking-wider bg-[linear-gradient(130deg,color-mix(in_srgb,hsl(var(--primary))_82%,hsl(var(--brand-surface))_18%),color-mix(in_srgb,hsl(var(--accent))_65%,hsl(var(--brand-surface))_35%))] border-[hsl(var(--primary)/0.45)] shadow-[0_12px_24px_rgba(0,0,0,0.4)] transition-[transform,box-shadow,border-color,background] duration-250 before:absolute before:inset-0 before:bg-[radial-gradient(circle_at_20%_20%,rgba(255,255,255,0.35),transparent_55%)] before:opacity-75 before:pointer-events-none before:mix-blend-screen hover:bg-[linear-gradient(130deg,color-mix(in_srgb,hsl(var(--primary))_88%,hsl(var(--brand-surface))_12%),color-mix(in_srgb,hsl(var(--accent))_72%,hsl(var(--brand-surface))_28%))] hover:border-[hsl(var(--primary)/0.6)] hover:shadow-[0_18px_34px_rgba(0,0,0,0.5)] hover:-translate-y-0.5 hover:before:opacity-95 [&_svg]:transition-transform [&_svg]:duration-250 hover:[&_svg]:translate-x-1 hover:[&_svg]:-translate-y-1",
			},
			size: {
				default: "h-9 px-4 py-2 has-[>svg]:px-3",
				sm: "h-8 gap-1.5 rounded-md px-3 has-[>svg]:px-2.5",
				lg: "h-10 rounded-md px-6 has-[>svg]:px-4",
				icon: "size-9",
				"icon-sm": "size-8",
				"icon-lg": "size-10",
			},
		},
		defaultVariants: {
			variant: "default",
			size: "default",
		},
	});

	export type ButtonVariant = VariantProps<typeof buttonVariants>["variant"];
	export type ButtonSize = VariantProps<typeof buttonVariants>["size"];

	export type ButtonProps = WithElementRef<HTMLButtonAttributes> &
		WithElementRef<HTMLAnchorAttributes> & {
			variant?: ButtonVariant;
			size?: ButtonSize;
		};
</script>

<script lang="ts">
	let {
		class: className,
		variant = "default",
		size = "default",
		ref = $bindable(null),
		href = undefined,
		type = "button",
		disabled,
		children,
		...restProps
	}: ButtonProps = $props();
</script>

{#if href}
	<a
		bind:this={ref}
		data-slot="button"
		class={cn(buttonVariants({ variant, size }), className)}
		href={disabled ? undefined : href}
		aria-disabled={disabled}
		role={disabled ? "link" : undefined}
		tabindex={disabled ? -1 : undefined}
		{...restProps}
	>
		{@render children?.()}
	</a>
{:else}
	<button
		bind:this={ref}
		data-slot="button"
		class={cn(buttonVariants({ variant, size }), className)}
		{type}
		{disabled}
		{...restProps}
	>
		{@render children?.()}
	</button>
{/if}
