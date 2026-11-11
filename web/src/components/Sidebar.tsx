import { ChevronsUpDown, PanelLeftClose, PanelLeftOpen, X } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { NavLink } from 'react-router'
import { errorCode } from '../lib/api.ts'
import { useSession } from '../lib/session.tsx'
import { cx, focusRing } from '../lib/styles.ts'
import { sections } from '../sections.ts'

type SidebarProps =
  | { variant: 'static'; collapsed: boolean; onToggle: () => void }
  | { variant: 'drawer'; onClose: () => void }

export function Sidebar(props: SidebarProps) {
  const drawer = props.variant === 'drawer'
  const collapsed = !drawer && props.collapsed
  const id = drawer ? 'sidebar-drawer' : 'sidebar'
  const ToggleIcon = drawer ? X : collapsed ? PanelLeftOpen : PanelLeftClose
  const rootRef = useRef<HTMLDivElement>(null)

  // A drawer takes focus when it opens.
  useEffect(() => {
    if (drawer) rootRef.current?.focus()
  }, [drawer])

  const rootClass = cx(
    'shrink-0 flex-col gap-6 border-r border-rule py-6',
    drawer
      ? 'relative flex h-full w-60 max-w-full bg-paper px-4 outline-none'
      : cx('sticky top-0 hidden h-svh md:flex', collapsed ? 'w-16 px-3' : 'w-60 px-4'),
  )

  const header = (
    <div
      className={cx('flex items-center', collapsed ? 'justify-center' : 'justify-between pl-2.5')}
    >
      {!collapsed && <div className="text-wordmark font-semibold">Permaily</div>}
      <button
        type="button"
        aria-label={drawer ? 'Close menu' : 'Sidebar'}
        {...(drawer ? {} : { 'aria-expanded': !collapsed, 'aria-controls': id })}
        onClick={drawer ? props.onClose : props.onToggle}
        className={cx('rounded-sm p-2 text-ink-2 hover:text-ink', focusRing)}
      >
        <ToggleIcon aria-hidden size={16} strokeWidth={1.75} />
      </button>
    </div>
  )

  const body = (
    <>
      {header}

      <nav aria-label="Main" className="flex flex-col gap-0.5">
        {sections.map(({ path, label, icon: Icon, waiting }) => (
          <NavLink
            key={path}
            to={path}
            end={path === '/'}
            title={collapsed ? label : undefined}
            onClick={drawer ? props.onClose : undefined}
            className={({ isActive }) =>
              cx(
                'relative flex items-center rounded-sm py-2 text-body',
                collapsed ? 'justify-center px-0' : 'gap-2.5 px-2.5',
                isActive
                  ? 'bg-sunken font-medium text-ink'
                  : 'text-ink-2 hover:bg-sunken hover:text-ink',
                focusRing,
              )
            }
          >
            <Icon aria-hidden size={16} strokeWidth={1.75} />
            <span className={collapsed ? 'sr-only' : 'flex-1'}>{label}</span>
            {waiting > 0 && (
              <span
                className={cx(
                  'font-mono font-medium text-attention',
                  collapsed ? 'absolute right-1 top-0.5 text-micro leading-none' : 'text-mono',
                )}
              >
                {waiting}
                <span className="sr-only"> waiting for you</span>
              </span>
            )}
          </NavLink>
        ))}
      </nav>

      <div className="flex-1" />

      <AccountMenu compact={collapsed} />
    </>
  )

  return drawer ? (
    <div
      ref={rootRef}
      id={id}
      role="dialog"
      aria-modal="true"
      aria-label="Menu"
      tabIndex={-1}
      className={rootClass}
    >
      {body}
    </div>
  ) : (
    <aside id={id} className={rootClass}>
      {body}
    </aside>
  )
}

/** Two letters for the collapsed sidebar: from the name, or from the address when the account has none. */
function initials(name: string, address: string): string {
  const words = name.split(/\s+/).filter(Boolean)
  if (words.length === 0) return address.slice(0, 2).toUpperCase()
  return words
    .slice(0, 2)
    .map((part) => part[0].toUpperCase())
    .join('')
}

/** Who is signed in, and the way out. */
function AccountMenu({ compact }: { compact: boolean }) {
  const { state, signOut } = useSession()
  const [open, setOpen] = useState(false)
  const [leaving, setLeaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const close = (event: MouseEvent | KeyboardEvent) => {
      if (
        event instanceof KeyboardEvent
          ? event.key === 'Escape'
          : !ref.current?.contains(event.target as Node)
      )
        setOpen(false)
    }
    document.addEventListener('mousedown', close)
    document.addEventListener('keydown', close)
    return () => {
      document.removeEventListener('mousedown', close)
      document.removeEventListener('keydown', close)
    }
  }, [open])

  if (state.status !== 'signedIn') return null
  const { name, address } = state.me.account
  const label = name || address

  async function leave() {
    if (leaving) return
    setLeaving(true)
    setError(null)
    try {
      await signOut()
    } catch (failure) {
      setLeaving(false)
      setError(
        errorCode(failure) === 'network'
          ? "Couldn't sign out: Permaily can't be reached. Check your connection, then try again."
          : "Couldn't sign out. Try again in a moment.",
      )
    }
  }

  return (
    <div ref={ref} className="relative border-t border-rule pt-3">
      {open && (
        <div
          role="menu"
          className={cx(
            'absolute bottom-full left-0 mb-2 flex min-w-32 flex-col border border-rule bg-raised py-1',
            compact ? 'w-56' : 'w-full',
          )}
        >
          {/* The whole address: the sidebar cuts a long one short, and shows only initials when collapsed. */}
          <p className="wrap-anywhere border-b border-rule px-3 pt-1 pb-2 font-mono text-mono text-ink-2">
            {address}
          </p>
          <button
            type="button"
            role="menuitem"
            onClick={leave}
            aria-disabled={leaving}
            className={cx('px-3 py-2 text-left text-body hover:bg-sunken', focusRing)}
          >
            Sign out
          </button>
          <p role="alert" className="px-3 pb-2 text-small text-danger empty:hidden">
            {error}
          </p>
        </div>
      )}
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        title={compact ? label : undefined}
        className={cx(
          'flex w-full items-center rounded-sm text-left',
          compact ? 'justify-center py-1.5' : cx('gap-2 px-2.5', !name && 'py-1.5'),
          focusRing,
        )}
      >
        {compact ? (
          <span aria-hidden className="font-mono text-mono font-medium">
            {initials(name, address)}
          </span>
        ) : (
          <>
            <span className="flex min-w-0 flex-1 flex-col gap-0.5">
              {name && <span className="truncate text-small font-medium">{name}</span>}
              <span className="truncate font-mono text-mono text-ink-2">{address}</span>
            </span>
            <ChevronsUpDown aria-hidden size={14} className="shrink-0 text-ink-2" />
          </>
        )}
        <span className="sr-only">Account menu{compact ? `, ${label}` : ''}</span>
      </button>
    </div>
  )
}
