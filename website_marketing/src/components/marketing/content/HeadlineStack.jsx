import { forwardRef } from 'react'
import { cn } from '@/lib/utils'
import MarketingStack from '../layout/MarketingStack'

const HeadlineStack = forwardRef(
  (
    {
      eyebrow,
      title,
      subtitle,
      kicker,
      actions,
      actionsPlacement = 'stack',
      align = 'left',
      underline = true,
      underlineAlign = 'auto',
      className,
      children,
      ...props
    },
    ref
  ) => {
    const stackAlign = align === 'center' ? 'center' : 'start'
    const resolvedUnderlineAlign = underlineAlign === 'auto' ? (stackAlign === 'center' ? 'center' : 'start') : underlineAlign
    const inlineActions = Boolean(actionsPlacement === 'inline')

    const eyebrowNode = eyebrow ? <span className="headline-stack__eyebrow marketing-pill">{eyebrow}</span> : null
    const titleNode = title ? <h2 className="headline-stack__title">{title}</h2> : null
    const kickerNode = kicker ? <span className="headline-stack__kicker">{kicker}</span> : null
    const subtitleNode = subtitle ? <p className="headline-stack__subtitle">{subtitle}</p> : null
    const stackedActionsNode =
      actions && !inlineActions ? <div className="headline-stack__actions">{actions}</div> : null
    const inlineActionsNode =
      inlineActions ? (
        <div className="headline-stack__actions headline-stack__actions--inline">{actions}</div>
      ) : null

    const copyContent = (
      <div className="headline-stack__copy">
        {eyebrowNode}
        {titleNode}
        {kickerNode}
      </div>
    )

    return (
      <MarketingStack
        ref={ref}
        align={stackAlign}
        className={cn(
          'headline-stack',
          inlineActions && 'headline-stack--inline',
          className
        )}
        data-align={align}
        data-underline={underline ? 'true' : 'false'}
        data-underline-align={resolvedUnderlineAlign}
        {...props}
      >
        {inlineActions ? (
          <>
            <div className="headline-stack__frame">
              {copyContent}
              {inlineActionsNode}
            </div>
            {subtitleNode}
            {children}
          </>
        ) : (
          <>
            {copyContent}
            {subtitleNode}
            {children}
            {stackedActionsNode}
          </>
        )}
      </MarketingStack>
    )
  }
)

export default HeadlineStack
