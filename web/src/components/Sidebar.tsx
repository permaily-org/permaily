import { ChevronsUpDown } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { NavLink } from 'react-router'
import { user } from '../data/sample.ts'
import { cx, focusRing } from '../lib/styles.ts'
import { sections } from '../sections.ts'

export function Sidebar() {
  return (
    <aside className="sticky top-0 flex h-svh w-60 shrink-0 flex-col gap-6 border-r border-rule px-4 py-6">
      <div className="px-2.5 text-wordmark font-semibold">Permaily</div>

      <nav aria-label="Main" className="flex flex-col gap-0.5">
        {sections.map(({ path, label, icon: Icon, waiting }) => (
          <NavLink
            key={path}
            to={path}
            end={path === '/'}
            className={({ isActive }) =>
              cx(
                'flex items-center gap-2.5 rounded-sm px-2.5 py-2 text-body',
                isActive ? 'bg-sunken font-medium text-ink' : 'text-ink-2 hover:bg-sunken hover:text-ink',
                focusRing,
              )
            }
          >
            <Icon aria-hidden size={16} strokeWidth={1.75} />
            <span className="flex-1">{label}</span>
            {waiting > 0 && (
              <span className="font-mono text-mono font-medium text-attention">
                {waiting}
                <span className="sr-only"> waiting for you</span>
              </span>
            )}
          </NavLink>
        ))}
      </nav>

      <div className="flex-1" />

      <AccountMenu />
    </aside>
  )
}

function AccountMenu() {
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
        <div role="menu" className="absolute bottom-full left-0 mb-2 flex w-full flex-col border border-rule bg-raised py-1">
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
        className={cx('flex w-full items-center gap-2 rounded-sm px-2.5 text-left', focusRing)}
      >
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="text-small font-medium">{user.name}</span>
          <span className="truncate font-mono text-mono text-ink-2">{user.email}</span>
        </span>
        <ChevronsUpDown aria-hidden size={14} className="text-ink-2" />
        <span className="sr-only">Account menu</span>
      </button>
    </div>
  )
}
