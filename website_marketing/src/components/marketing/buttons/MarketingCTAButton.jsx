import { forwardRef } from 'react'
import { cn } from '@/lib/utils'
import { Link } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import { ArrowTopRightOnSquareIcon } from '@heroicons/react/24/outline'
import { isProbablyExternalHref } from '../utils'

const MarketingCTAButton = forwardRef(
  (
    {
      intent = 'secondary',
      label,
      icon = 'auto',
      external,
      href,
      to,
      rel,
      target,
      onClick,
      className,
      size = 'lg',
      variant,
      children,
      ...props
    },
    ref
  ) => {
    const buttonLabel = children ?? label
    if (!buttonLabel) return null

    const isExternal = Boolean(external ?? isProbablyExternalHref(href))
    const IconComponent = typeof icon === 'function' ? icon : null
    const shouldShowAutoIcon = icon === 'auto' && isExternal
    const shouldShowIcon = Boolean(IconComponent) || shouldShowAutoIcon
    const resolvedVariant = variant ?? (intent === 'primary' ? 'default' : 'secondary')

    const buttonClasses = cn(
      'marketing-cta__button',
      intent === 'primary' ? 'cta-button' : 'marketing-cta__button--secondary',
      intent !== 'primary' && 'cta-motion',
      shouldShowAutoIcon && 'marketing-cta__button--arrow',
      className
    )

    const content = (
      <span className="marketing-cta__link">
        <span>{buttonLabel}</span>
        {shouldShowIcon ? (
          IconComponent ? (
            <IconComponent className="w-4 h-4 cta-button__icon" />
          ) : (
            <ArrowTopRightOnSquareIcon className="w-4 h-4 cta-button__icon" />
          )
        ) : null}
      </span>
    )

    if (href) {
      const resolvedTarget = target ?? (isExternal ? '_blank' : undefined)
      const resolvedRel = rel ?? (isExternal ? 'noreferrer noopener' : undefined)

      return (
        <Button ref={ref} asChild className={buttonClasses} size={size} variant={resolvedVariant} {...props}>
          <a href={href} target={resolvedTarget} rel={resolvedRel} onClick={onClick}>
            {content}
          </a>
        </Button>
      )
    }

    if (to) {
      return (
        <Button ref={ref} asChild className={buttonClasses} size={size} variant={resolvedVariant} {...props}>
          <Link to={to} onClick={onClick}>
            {content}
          </Link>
        </Button>
      )
    }

    return (
      <Button ref={ref} className={buttonClasses} size={size} variant={resolvedVariant} onClick={onClick} {...props}>
        {content}
      </Button>
    )
  }
)

export default MarketingCTAButton
