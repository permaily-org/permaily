import { ChevronsUpDown, PanelLeftClose, PanelLeftOpen, X } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { NavLink } from 'react-router'
import { user } from '../data/sample.ts'
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
    <div className={cx('flex items-center', collapsed ? 'justify-center' : 'justify-between pl-2.5')}>
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
                isActive ? 'bg-sunken font-medium text-ink' : 'text-ink-2 hover:bg-sunken hover:text-ink',
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
    <div ref={rootRef} id={id} role="dialog" aria-modal="true" aria-label="Menu" tabIndex={-1} className={rootClass}>
      {body}
    </div>
  ) : (
    <aside id={id} className={rootClass}>
      {body}
    </aside>
  )
}

function initials(name: string): string {
  return name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((part) => part[0].toUpperCase())
    .join('')
}

function AccountMenu({ compact }: { compact: boolean }) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const close = (event: MouseEvent | KeyboardEvent) => {
      if (event instanceof KeyboardEvent ? event.key === 'Escape' : !ref.current?.contains(event.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', close)
    document.addEventListener('keydown', close)
    return () => {
      document.removeEventListener('mousedown', close)
      document.removeEventListener('keydown', close)
    }
  }, [open])

  return (
    <div ref={ref} className="relative border-t border-rule pt-3">
      {open && (
        <div role="menu" className="absolute bottom-full left-0 mb-2 flex w-full min-w-32 flex-col border border-rule bg-raised py-1">
          <button type="button" role="menuitem" className={cx('px-3 py-2 text-left text-body hover:bg-sunken', focusRing)}>
            Sign out
          </button>
        </div>
      )}
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        title={compact ? user.name : undefined}
        className={cx(
          'flex w-full items-center rounded-sm text-left',
          compact ? 'justify-center py-1.5' : 'gap-2 px-2.5',
          focusRing,
        )}
      >
        {compact ? (
          <span aria-hidden className="font-mono text-mono font-medium">
            {initials(user.name)}
          </span>
        ) : (
          <>
            <span className="flex min-w-0 flex-1 flex-col gap-0.5">
              <span className="text-small font-medium">{user.name}</span>
              <span className="truncate font-mono text-mono text-ink-2">{user.email}</span>
            </span>
            <ChevronsUpDown aria-hidden size={14} className="text-ink-2" />
          </>
        )}
        <span className="sr-only">Account menu</span>
      </button>
    </div>
  )
}
