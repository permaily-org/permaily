import { NavLink } from 'react-router'
import { cx, focusRing } from '../../lib/styles.ts'

const tabs = [
  { to: '/rules', label: 'Your rules', end: true },
  { to: '/rules/packs', label: 'Community packs', end: false },
]

export function RulesTabs() {
  return (
    <nav aria-label="Rules" className="flex gap-6 border-b border-rule">
      {tabs.map((tab) => (
        <NavLink
          key={tab.to}
          to={tab.to}
          end={tab.end}
          className={({ isActive }) =>
            cx(
              '-mb-px border-b-2 pb-2.5 text-body',
              isActive ? 'border-ink font-medium text-ink' : 'border-transparent text-ink-2 hover:text-ink',
              focusRing,
            )
          }
        >
          {tab.label}
        </NavLink>
      ))}
    </nav>
  )
}
