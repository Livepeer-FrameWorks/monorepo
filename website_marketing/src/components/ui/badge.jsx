import { forwardRef } from 'react'
import { cva } from 'class-variance-authority'
import { cn } from '@/lib/utils'

const badgeVariants = cva(
  'inline-flex items-center gap-1 rounded-full border px-3 py-1 text-xs font-medium uppercase tracking-[0.2em] transition-colors',
  {
    variants: {
      variant: {
        default: 'border border-transparent bg-accent text-accent-foreground shadow-brand-subtle',
        outline: 'border border-border/60 text-foreground',
        glow: 'badge-glow',
      },
      tone: {
        accent: 'badge-tone-accent',
        purple: 'badge-tone-purple',
        green: 'badge-tone-green',
        yellow: 'badge-tone-yellow',
        cyan: 'badge-tone-cyan',
        orange: 'badge-tone-orange',
        neutral: 'badge-tone-neutral',
      },
    },
    compoundVariants: [
      {
        variant: 'default',
        tone: 'purple',
        className: 'bg-[hsl(var(--brand-accent-soft)/0.55)] text-foreground',
      },
      {
        variant: 'default',
        tone: 'green',
        className: 'bg-[hsl(var(--brand-green)/0.18)] text-brand-green',
      },
      {
        variant: 'default',
        tone: 'yellow',
        className: 'bg-[hsl(var(--brand-yellow)/0.22)] text-brand-yellow',
      },
      {
        variant: 'default',
        tone: 'cyan',
        className: 'bg-[hsl(var(--brand-cyan)/0.2)] text-brand-cyan',
      },
      {
        variant: 'default',
        tone: 'orange',
        className: 'bg-[hsl(var(--brand-orange)/0.22)] text-brand-orange',
      },
      {
        variant: 'default',
        tone: 'neutral',
        className: 'bg-background/70 text-foreground border-border/40',
      },
      {
        variant: 'outline',
        tone: 'accent',
        className: 'text-accent border-[hsl(var(--accent)/0.6)]',
      },
      {
        variant: 'outline',
        tone: 'purple',
        className: 'text-brand-purple border-[hsl(var(--brand-accent-strong)/0.6)]',
      },
      {
        variant: 'outline',
        tone: 'green',
        className: 'text-brand-green border-[hsl(var(--brand-green)/0.6)]',
      },
      {
        variant: 'outline',
        tone: 'yellow',
        className: 'text-brand-yellow border-[hsl(var(--brand-yellow)/0.6)]',
      },
      {
        variant: 'outline',
        tone: 'cyan',
        className: 'text-brand-cyan border-[hsl(var(--brand-cyan)/0.6)]',
      },
      {
        variant: 'outline',
        tone: 'orange',
        className: 'text-brand-orange border-[hsl(var(--brand-orange)/0.6)]',
      },
    ],
    defaultVariants: {
      variant: 'default',
      tone: 'accent',
    },
  }
)

const Badge = forwardRef(({ className, variant, tone, ...props }, ref) => (
  <span ref={ref} className={cn(badgeVariants({ variant, tone }), className)} {...props} />
))

Badge.displayName = 'Badge'

export { Badge, badgeVariants }
