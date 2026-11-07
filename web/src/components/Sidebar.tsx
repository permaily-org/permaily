import { ChevronsUpDown } from 'lucide-react'
import { NavLink } from 'react-router'
import { sections } from '../sections.ts'

const focusRing = 'focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-ink'

export function Sidebar() {
  return (
    <aside className="flex w-60 shrink-0 flex-col gap-6 border-r border-rule px-4 py-6">
      <div className="px-2.5 text-wordmark font-semibold">Permaily</div>

      <nav aria-label="Main" className="flex flex-col gap-0.5">
        {sections.map(({ path, label, icon: Icon }) => (
          <NavLink
            key={path}
            to={path}
            end={path === '/'}
            className={({ isActive }) =>
              [
                'flex items-center gap-2.5 rounded-sm px-2.5 py-2 text-body',
                isActive ? 'bg-sunken font-medium text-ink' : 'text-ink-2 hover:bg-sunken hover:text-ink',
                focusRing,
              ].join(' ')
            }
          >
            <Icon aria-hidden size={16} strokeWidth={1.75} />
            {label}
          </NavLink>
        ))}
      </nav>

      <div className="flex-1" />

      <button
        type="button"
        className={`flex w-full items-center gap-2 rounded-sm border-t border-rule px-2.5 pt-3 text-left ${focusRing}`}
      >
        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="text-small font-medium">Marta Ruiz</span>
          <span className="truncate font-mono text-mono text-ink-2">marta@example.com</span>
        </span>
        <ChevronsUpDown aria-hidden size={14} className="text-ink-2" />
        <span className="sr-only">Open account menu</span>
      </button>
    </aside>
  )
}
