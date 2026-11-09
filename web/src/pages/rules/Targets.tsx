import { Plus, X } from 'lucide-react'
import {
  type CSSProperties,
  Fragment,
  type KeyboardEvent,
  type ReactNode,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from 'react'
import { Tile } from '../../components/ui/index.ts'
import { connectionsOf, isConnectionTarget, targetLabel, vendors } from '../../data/sample.ts'
import { cx, focusRing } from '../../lib/styles.ts'

/**
 * The vendors and connections a rule names, as chips, with a menu to add more. Picking a vendor
 * covers all of its connections, including ones it adds later, so it replaces any of them picked
 * one by one.
 */
export function Targets({
  label,
  targets,
  onChange,
}: {
  label: string
  targets: string[]
  onChange: (targets: string[]) => void
}) {
  const focusAfterRemove = useRef<number | null>(null)
  const listRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const index = focusAfterRemove.current
    if (index === null) return
    focusAfterRemove.current = null
    const removes = listRef.current?.querySelectorAll<HTMLElement>('[data-remove]')
    const next =
      removes?.[index] ??
      removes?.[index - 1] ??
      listRef.current?.querySelector<HTMLElement>('[data-add]')
    next?.focus()
  })

  function add(target: string) {
    const kept = isConnectionTarget(target)
      ? targets
      : targets.filter((t) => t.split('/')[0] !== target)
    onChange([...kept, target])
  }

  function remove(index: number) {
    focusAfterRemove.current = index
    onChange(targets.filter((_, i) => i !== index))
  }

  return (
    <div
      ref={listRef}
      role="group"
      aria-label={label}
      className="flex min-w-0 flex-1 flex-wrap items-center gap-x-1.5 gap-y-2"
    >
      {targets.map((target, i) => (
        // Like a condition's chip: cut short rather than pushing past its row, with 24px to tap around the remove icon.
        <span
          key={target}
          className="inline-flex h-[22px] max-w-full items-center gap-0.5 rounded-sm bg-sunken pr-1 pl-2 text-small has-[button:focus-visible]:outline-2 has-[button:focus-visible]:outline-offset-2 has-[button:focus-visible]:outline-ink"
        >
          <span className="min-w-0 truncate">{targetLabel(target)}</span>
          <button
            type="button"
            data-remove
            onClick={() => remove(i)}
            aria-label={`Remove ${targetLabel(target)}`}
            className="relative inline-flex size-3.5 shrink-0 items-center justify-center rounded-[1px] text-ink-2 outline-none before:absolute before:-inset-[5px] hover:bg-rule hover:text-ink"
          >
            <X aria-hidden size={12} strokeWidth={2} />
          </button>
        </span>
      ))}
      <TargetMenu label={`Add to ${label.toLowerCase()}`} taken={targets} onPick={add} />
    </div>
  )
}

const menuWidth = 264
const gap = 4

/**
 * The menu is fixed to the viewport, so the editor's scrolling column can't clip it. It opens
 * below the button, or above when there's no room, and closes if anything scrolls.
 */
function TargetMenu({
  label,
  taken,
  onPick,
}: {
  label: string
  taken: string[]
  onPick: (target: string) => void
}) {
  const [open, setOpen] = useState(false)
  const [style, setStyle] = useState<CSSProperties>({ width: menuWidth })
  const wrapRef = useRef<HTMLDivElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const buttonRef = useRef<HTMLButtonElement>(null)

  useLayoutEffect(() => {
    if (!open) return
    const button = buttonRef.current?.getBoundingClientRect()
    const height = menuRef.current?.offsetHeight ?? 0
    if (!button) return
    const below = button.bottom + gap + height <= window.innerHeight - 8
    setStyle({
      top: below ? button.bottom + gap : Math.max(8, button.top - gap - height),
      left: Math.max(8, Math.min(button.left, window.innerWidth - menuWidth - 8)),
      width: menuWidth,
    })
  }, [open])

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
      setStyle({ width: menuWidth })
    }
  }, [open])

  function closeMenu() {
    setOpen(false)
    buttonRef.current?.focus()
  }

  function pick(target: string) {
    onPick(target)
    closeMenu()
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
      closeMenu()
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
        aria-label={label}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className={cx(
          'relative inline-flex size-[22px] items-center justify-center rounded-sm border border-rule text-ink-2 before:absolute before:-inset-0.5 hover:border-ink hover:text-ink',
          focusRing,
        )}
      >
        <Plus aria-hidden size={14} />
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
          {vendors.map((vendor) => {
            const list = connectionsOf(vendor.id)
            const vendorTaken = taken.includes(vendor.id)
            return (
              <Fragment key={vendor.id}>
                <MenuItem disabled={vendorTaken} onSelect={() => pick(vendor.id)}>
                  <Tile initials={vendor.initials} size="sm" />
                  <span className="min-w-0 flex-1 truncate font-medium">{vendor.name}</span>
                  {list.length > 1 && (
                    <span className="font-mono text-mono text-ink-2">all {list.length}</span>
                  )}
                </MenuItem>
                {list.length > 1 &&
                  list.map((connection) => (
                    <MenuItem
                      key={connection.id}
                      disabled={vendorTaken || taken.includes(connection.id)}
                      onSelect={() => pick(connection.id)}
                      indent
                    >
                      <span className="min-w-0 flex-1 truncate">{connection.name}</span>
                    </MenuItem>
                  ))}
              </Fragment>
            )
          })}
        </div>
      )}
    </div>
  )
}

function MenuItem({
  children,
  disabled,
  indent,
  onSelect,
}: {
  children: ReactNode
  disabled: boolean
  indent?: boolean
  onSelect: () => void
}) {
  return (
    <button
      type="button"
      role="menuitem"
      disabled={disabled}
      onClick={onSelect}
      className={cx(
        'flex w-full items-center gap-2 py-1.5 pr-3 text-left text-small outline-none hover:bg-sunken focus-visible:bg-sunken disabled:cursor-default disabled:text-ink-3 disabled:hover:bg-transparent',
        indent ? 'pl-[44px]' : 'pl-3',
      )}
    >
      {children}
    </button>
  )
}
