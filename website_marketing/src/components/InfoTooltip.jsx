import React, { useId, useRef, useState, useEffect } from 'react'

// Accessible tooltip with hover and keyboard support
const InfoTooltip = ({ children, label = 'More info', position = 'top', className = '' }) => {
  const id = useId()
  const [open, setOpen] = useState(false)
  const triggerRef = useRef(null)
  const tipRef = useRef(null)

  useEffect(() => {
    if (!open) return
    const onKey = (e) => {
      if (e.key === 'Escape') setOpen(false)
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open])

  const posClass = position === 'bottom'
    ? 'top-full mt-2 left-1/2 -translate-x-1/2'
    : position === 'left'
      ? 'right-full mr-2 top-1/2 -translate-y-1/2'
      : position === 'right'
        ? 'left-full ml-2 top-1/2 -translate-y-1/2'
        : 'bottom-full mb-2 left-1/2 -translate-x-1/2'

  return (
    <span className={`relative inline-flex ${className}`}>
      <button
        type="button"
        ref={triggerRef}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? id : undefined}
        aria-label={label}
        onMouseEnter={() => setOpen(true)}
        onMouseLeave={() => setOpen(false)}
        onFocus={() => setOpen(true)}
        onBlur={(e) => {
          // Close when focus leaves both button and tooltip
          if (!e.currentTarget.contains(e.relatedTarget) && !tipRef.current?.contains(e.relatedTarget)) {
            setOpen(false)
          }
        }}
        className="w-4 h-4 rounded-full border border-tokyo-night-comment/60 text-tokyo-night-comment hover:text-tokyo-night-fg flex items-center justify-center text-[10px] focus:outline-none focus:ring-2 focus:ring-tokyo-night-blue/50"
      >
        i
      </button>
      {open && (
        <div
          id={id}
          role="dialog"
          ref={tipRef}
          className={`absolute z-[9999] ${posClass} w-max max-w-[90vw] sm:max-w-xs p-3 rounded-md text-xs bg-tokyo-night-bg-dark border border-tokyo-night-fg-gutter text-tokyo-night-fg shadow-xl`}
          onMouseEnter={() => setOpen(true)}
          onMouseLeave={() => setOpen(false)}
        >
          {children}
        </div>
      )}
    </span>
  )
}

export default InfoTooltip

