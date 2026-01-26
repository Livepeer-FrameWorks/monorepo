import { forwardRef } from 'react'
import { cn } from '@/lib/utils'

const CTACluster = forwardRef(
  ({ align = 'start', wrap = false, className, children, ...props }, ref) => (
    <div
      ref={ref}
      className={cn(
        'marketing-cta-cluster',
        align && `marketing-cta-cluster--align-${align}`,
        wrap && 'marketing-cta-cluster--wrap',
        className
      )}
      {...props}
    >
      {children}
    </div>
  )
)

CTACluster.displayName = 'CTACluster'

export default CTACluster
