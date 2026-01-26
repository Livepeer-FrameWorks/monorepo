import { forwardRef } from 'react'
import { cn } from '@/lib/utils'
import { renderSlot } from '../utils'

const MarketingCodePanel = forwardRef(
  (
    {
      badge,
      badgeTone = 'accent',
      language,
      code,
      actions,
      className,
      children,
      variant = 'panel',
      ...props
    },
    ref
  ) => {
    const panelClass = cn(
      'marketing-code-panel',
      variant === 'plain' && 'marketing-code-panel--plain',
      className
    )

    const headerClass = cn(
      'marketing-code-panel__header',
      variant === 'plain' && 'marketing-code-panel__header--plain'
    )

    const bodyClass = cn(
      'marketing-code-panel__body',
      variant === 'plain' && 'marketing-code-panel__body--plain'
    )

    return (
      <div ref={ref} className={panelClass} {...props}>
        {(badge || language || actions) ? (
          <div className={headerClass}>
            <div className="marketing-code-panel__headline">
              {badge ? (
                <span className={cn('marketing-code-panel__badge', badgeTone && `badge-tone-${badgeTone}`)}>{badge}</span>
              ) : null}
              {language ? <span className="marketing-code-panel__language">{language}</span> : null}
            </div>
            {actions ? <div className="marketing-code-panel__actions">{renderSlot(actions)}</div> : null}
          </div>
        ) : null}
        <div className={bodyClass}>
          {code ? (
            <pre>
              <code>{code}</code>
            </pre>
          ) : null}
          {children}
        </div>
      </div>
    )
  }
)

MarketingCodePanel.displayName = 'MarketingCodePanel'

export default MarketingCodePanel
