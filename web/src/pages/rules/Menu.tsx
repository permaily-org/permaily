import {
  type CSSProperties,
  createContext,
  type KeyboardEvent,
  type ReactNode,
  useContext,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from 'react'
import { cx } from '../../lib/styles.ts'

const gap = 4

/** Closes the open menu, putting focus back on its button unless focus is going somewhere else. */
const CloseMenu = createContext<(refocus: boolean) => void>(() => {})

/**
 * A button and the menu it opens. The menu is fixed to the viewport, so the editor's scrolling column can't clip it.
 * It opens below the button, or above when there's no room, and closes if anything scrolls.
 */
export function MenuButton({
  label,
  iconOnly = false,
  buttonLabel,
  button,
  buttonClassName,
  width = 264,
  children,
}: {
  /** Names the menu, and the button too when it shows only an icon. */
  label: string
  iconOnly?: boolean
  /** A fuller name for a button whose text is short, such as "And". It should start with that text. */
  buttonLabel?: string
  button: ReactNode
  buttonClassName: string
  width?: number
  children: ReactNode
}) {
  const [open, setOpen] = useState(false)
  const [style, setStyle] = useState<CSSProperties>({ width })
  const wrapRef = useRef<HTMLDivElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const buttonRef = useRef<HTMLButtonElement>(null)

  useLayoutEffect(() => {
    if (!open) return
    const rect = buttonRef.current?.getBoundingClientRect()
    const height = menuRef.current?.offsetHeight ?? 0
    if (!rect) return
    const below = rect.bottom + gap + height <= window.innerHeight - 8
    setStyle({
      top: below ? rect.bottom + gap : Math.max(8, rect.top - gap - height),
      left: Math.max(8, Math.min(rect.left, window.innerWidth - width - 8)),
      width,
    })
  }, [open, width])

  useEffect(() => {
    if (!open) return
    menuRef.current
      ?.querySelector<HTMLElement>('[role="menuitem"]:not(:disabled)')
      ?.focus({ preventScroll: true })
    const closeOutside = (event: MouseEvent) => {
      if (!wrapRef.current?.contains(event.target as Node)) setOpen(false)
    }
    const close = () => setOpen(false)
    document.addEventListener('mousedown', closeOutside)
    window.addEventListener('scroll', close, true)
    window.addEventListener('resize', close)
    return () => {
      document.removeEventListener('mousedown', closeOutside)
      window.removeEventListener('scroll', close, true)
      window.removeEventListener('resize', close)
      setStyle({ width })
    }
  }, [open, width])

  function close(refocus = true) {
    setOpen(false)
    if (refocus) buttonRef.current?.focus()
  }

  function onKeyDown(event: KeyboardEvent<HTMLDivElement>) {
    const items = [
      ...(menuRef.current?.querySelectorAll<HTMLButtonElement>(
        '[role="menuitem"]:not(:disabled)',
      ) ?? []),
    ]
    const at = items.indexOf(document.activeElement as HTMLButtonElement)
    const moves: Record<string, number> = {
      ArrowDown: at + 1,
      ArrowUp: at - 1,
      Home: 0,
      End: items.length - 1,
    }
    const move = moves[event.key]
    if (move !== undefined) {
      event.preventDefault()
      items[(move + items.length) % items.length]?.focus()
    } else if (event.key === 'Escape') {
      event.preventDefault()
      close()
    } else if (event.key === 'Tab') {
      setOpen(false)
    }
  }

  return (
    <div ref={wrapRef} className="relative">
      <button
        ref={buttonRef}
        type="button"
        data-add
        aria-label={iconOnly ? label : buttonLabel}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className={buttonClassName}
      >
        {button}
      </button>
      {open && (
        <div
          ref={menuRef}
          role="menu"
          aria-label={label}
          onKeyDown={onKeyDown}
          style={style}
          className="fixed z-30 border border-rule-strong bg-raised py-1"
        >
          <CloseMenu value={close}>{children}</CloseMenu>
        </div>
      )}
    </div>
  )
}

/** Picking an item closes its menu. refocus={false} is for an item that moves focus somewhere else, such as a new field. */
export function MenuItem({
  children,
  disabled = false,
  indent = false,
  refocus = true,
  onSelect,
}: {
  children: ReactNode
  disabled?: boolean
  indent?: boolean
  refocus?: boolean
  onSelect: () => void
}) {
  const close = useContext(CloseMenu)
  return (
    <button
      type="button"
      role="menuitem"
      disabled={disabled}
      onClick={() => {
        close(refocus)
        onSelect()
      }}
      className={cx(
        'flex w-full items-center gap-2 py-1.5 pr-3 text-left text-small outline-none hover:bg-sunken focus-visible:bg-sunken disabled:cursor-default disabled:text-ink-3 disabled:hover:bg-transparent',
        indent ? 'pl-[44px]' : 'pl-3',
      )}
    >
      {children}
    </button>
  )
}
