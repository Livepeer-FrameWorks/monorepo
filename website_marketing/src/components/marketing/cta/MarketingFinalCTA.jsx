import { cn } from '@/lib/utils'
import MarketingCTAButton from '../buttons/MarketingCTAButton'
import MarketingSlab from '../content/MarketingSlab'

const MarketingFinalCTA = ({
  eyebrow,
  title,
  description,
  primaryAction,
  secondaryAction,
  alignment = 'center',
  variant = 'slab',
  className,
}) => {
  const alignmentClass = alignment === 'left' ? 'marketing-cta--left' : 'marketing-cta--center'
  const secondaryActions = Array.isArray(secondaryAction)
    ? secondaryAction
    : secondaryAction
    ? [secondaryAction]
    : []

  const renderAction = (action, intent, index = 0) => {
    if (!action?.label) return null

    const { label, className: actionClassName, icon, key: actionKey, ...rest } = action

    return (
      <MarketingCTAButton
        key={actionKey ?? label ?? `cta-action-${intent}-${index}`}
        intent={intent}
        label={label}
        icon={icon}
        className={actionClassName}
        {...rest}
      />
    )
  }

  const content = (
    <div className={cn('marketing-cta', alignmentClass, variant === 'band' ? 'marketing-cta--band' : null)}>
      <div className="marketing-cta__body">
        {eyebrow ? <span className="marketing-pill marketing-cta__pill">{eyebrow}</span> : null}
        {title ? <h2 className="marketing-cta__title">{title}</h2> : null}
        {description ? <p className="marketing-cta__description">{description}</p> : null}
      </div>
      <div className="marketing-cta__actions">
        {renderAction(primaryAction, 'primary')}
        {secondaryActions.map((action, index) => renderAction(action, 'secondary', index))}
      </div>
    </div>
  )

  if (variant === 'band') {
    return <div className={cn('marketing-cta-band', className)}>{content}</div>
  }

  return (
    <MarketingSlab variant="cta-panel" className={className}>
      {content}
    </MarketingSlab>
  )
}

export default MarketingFinalCTA
