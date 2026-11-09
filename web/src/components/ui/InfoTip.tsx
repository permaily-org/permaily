import { Info } from 'lucide-react'
import { type ReactNode, useId, useLayoutEffect, useRef, useState } from 'react'
import { cx, focusRing } from '../../lib/styles.ts'

/** The closest a tip comes to the edge of the window. */
const edge = 8

/**
 * A fact worth having but not worth the space: an info icon that shows it on hover, focus or tap.
 * Use it sparingly, for facts people may need, never to explain the page.
 */
export function InfoTip({
  children,
  label = 'More about this',
  align = 'center',
}: {
  children: ReactNode
  label?: string
  align?: 'start' | 'center' | 'end'
}) {
  const id = useId()
  const [open, setOpen] = useState(false)
  const tipRef = useRef<HTMLSpanElement>(null)

  // Near the edge of a narrow window, as on a phone, the tip moves sideways until it fits.
  useLayoutEffect(() => {
    const tip = tipRef.current
    if (!open || !tip) return
    tip.style.transform = ''
    const { left, right } = tip.getBoundingClientRect()
    const over = right - (window.innerWidth - edge)
    const shift = over > 0 ? -over : left < edge ? edge - left : 0
    tip.style.transform = shift ? `translateX(${shift}px)` : ''
  }, [open])

  return (
    <span
      className="relative inline-flex align-middle"
      onMouseEnter={() => setOpen(true)}
      onMouseLeave={() => setOpen(false)}
    >
      <button
        type="button"
        aria-label={label}
        aria-describedby={id}
        onFocus={() => setOpen(true)}
        onBlur={() => setOpen(false)}
        onClick={() => setOpen((v) => !v)}
        onKeyDown={(event) => {
          if (event.key === 'Escape') setOpen(false)
        }}
        // An invisible margin makes the 16px icon a 24px touch target.
        className={cx(
          'relative inline-flex size-4 items-center justify-center rounded-full text-ink-3 after:absolute after:-inset-1 hover:text-ink',
          focusRing,
        )}
      >
        <Info aria-hidden size={14} />
      </button>
      <span
        ref={tipRef}
        role="tooltip"
        id={id}
        hidden={!open}
        className={cx(
          'absolute top-full z-30 mt-1.5 w-max max-w-[min(280px,calc(100vw-16px))] rounded-sm bg-ink px-2.5 py-1.5 text-left font-sans text-small font-normal text-paper normal-case',
          align === 'center' && 'left-1/2 -translate-x-1/2',
          align === 'start' && '-left-1',
          align === 'end' && '-right-1',
        )}
      >
        {children}
      </span>
    </span>
  )
}
