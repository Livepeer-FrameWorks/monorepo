import { cn } from '@/lib/utils'
import { Badge } from '@/components/ui/badge'

export const SECTION_SPACING = 'py-16 sm:py-24'

export function Section({ className, ...props }) {
  return <section className={cn(SECTION_SPACING, className)} {...props} />
}

export function SectionContainer({ className, ...props }) {
  return <div className={cn('mx-auto w-full max-w-7xl px-6 sm:px-8', className)} {...props} />
}

export function SectionHeader({
  eyebrow,
  title,
  description,
  align = 'center',
  className,
  children,
  ...props
}) {
  const alignment = align === 'left' ? 'items-start text-left' : 'items-center text-center'

  return (
    <div className={cn('flex flex-col gap-4', alignment, className)} {...props}>
      {eyebrow && (
        <Badge variant="outline" className="rounded-full border-border/40 bg-background/80">
          {eyebrow}
        </Badge>
      )}
      {title && (
        <h2 className="max-w-3xl text-balance text-3xl font-semibold tracking-tight sm:text-4xl md:text-5xl">
          {title}
        </h2>
      )}
      {description && (
        <p className="max-w-3xl text-pretty text-base text-muted-foreground sm:text-lg">
          {description}
        </p>
      )}
      {children}
    </div>
  )
}
