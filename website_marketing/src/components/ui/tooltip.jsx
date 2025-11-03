import * as React from 'react'
import * as TooltipPrimitive from '@radix-ui/react-tooltip'
import { cn } from '@/lib/utils'

const TooltipProvider = TooltipPrimitive.Provider
const Tooltip = TooltipPrimitive.Root
const TooltipTrigger = TooltipPrimitive.Trigger
const TooltipPortal = TooltipPrimitive.Portal

const TooltipContent = React.forwardRef(({ className, sideOffset = 8, ...props }, ref) => (
  <TooltipPortal>
    <TooltipPrimitive.Content
      ref={ref}
      sideOffset={sideOffset}
      className={cn(
        'z-50 overflow-hidden rounded-lg border px-3 py-2 text-xs leading-relaxed tooltip-surface data-[state=delayed-open]:animate-in data-[state=delayed-open]:fade-in data-[state=closed]:animate-out data-[state=closed]:fade-out data-[state=delayed-open]:zoom-in-95 data-[state=closed]:zoom-out-95',
        className
      )}
      {...props}
    />
  </TooltipPortal>
))
TooltipContent.displayName = TooltipPrimitive.Content.displayName

export { Tooltip, TooltipTrigger, TooltipContent, TooltipProvider }
