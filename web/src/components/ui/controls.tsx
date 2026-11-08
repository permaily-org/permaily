import { Check, ChevronDown, Search, X } from 'lucide-react'
import type { InputHTMLAttributes, ReactNode } from 'react'
import { cx, focusRing } from '../../lib/styles.ts'

/** Segmented choice between views, such as Whole inbox and Only shared. */
export function ViewSwitch<T extends string>({
  options,
  value,
  onChange,
  label,
  size = 'md',
}: {
  options: Array<{ value: T; label: ReactNode }>
  value: T
  onChange: (value: T) => void
  label: string
  /** sm fits beside a section heading. */
  size?: 'md' | 'sm'
}) {
  return (
    <div role="radiogroup" aria-label={label} className="inline-flex">
      {options.map((option, i) => {
        const selected = option.value === value
        return (
          <button
            key={option.value}
            type="button"
            role="radio"
            aria-checked={selected}
            onClick={() => onChange(option.value)}
            className={cx(
              'border border-ink font-medium',
              size === 'sm' ? 'px-2 py-px text-mono' : 'px-[11px] py-[5px] text-small',
              i > 0 && '-ml-px',
              selected ? 'bg-ink text-paper' : 'text-ink hover:bg-sunken',
              focusRing,
            )}
          >
            {option.label}
          </button>
        )
      })}
    </div>
  )
}

/** Looks like a dropdown trigger. Wire it to a menu when the page needs one. */
export function FilterButton({ children, onClick }: { children: ReactNode; onClick?: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={cx('inline-flex items-center gap-1.5 rounded-sm border border-rule px-2.5 py-[5px] text-small hover:border-ink', focusRing)}
    >
      {children}
      <ChevronDown aria-hidden size={14} className="text-ink-2" />
    </button>
  )
}

export function SearchField({ label, className, ...props }: InputHTMLAttributes<HTMLInputElement> & { label: string }) {
  return (
    <label className={cx('flex items-center gap-2 rounded-sm border border-ink px-2.5 py-[5px] focus-within:outline-2 focus-within:outline-offset-2 focus-within:outline-ink', className)}>
      <Search aria-hidden size={14} className="shrink-0 text-ink-2" />
      <span className="sr-only">{label}</span>
      <input type="search" {...props} className="min-w-0 flex-1 bg-transparent text-small outline-none placeholder:text-ink-3" />
    </label>
  )
}

export function Checkbox({ label, className, ...props }: InputHTMLAttributes<HTMLInputElement> & { label: ReactNode }) {
  return (
    <label className={cx('inline-flex cursor-pointer items-center gap-2.5', className)}>
      <span className="relative inline-flex size-[18px] shrink-0">
        <input
          type="checkbox"
          {...props}
          className={cx('peer size-[18px] cursor-pointer appearance-none rounded-sm border border-ink bg-transparent checked:bg-ink', focusRing)}
        />
        <Check aria-hidden size={14} strokeWidth={2.5} className="pointer-events-none absolute top-0.5 left-0.5 hidden text-paper peer-checked:block" />
      </span>
      <span className="min-w-0 flex-1">{label}</span>
    </label>
  )
}

export function Switch({ checked, onChange, label }: { checked: boolean; onChange: (checked: boolean) => void; label: string }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      onClick={() => onChange(!checked)}
      className={cx('relative inline-flex h-[18px] w-[34px] shrink-0 items-center rounded-sm border border-ink', checked ? 'bg-ink' : 'bg-paper', focusRing)}
    >
      <span className={cx('absolute size-3 rounded-[1px] transition-[left]', checked ? 'left-[18px] bg-paper' : 'left-0.5 bg-ink-3')} />
    </button>
  )
}

export type StepState = 'done' | 'current' | 'next' | 'failed'

export function StepMarker({ state, number }: { state: StepState; number: number }) {
  const label = { done: 'Done', current: 'Current step', next: 'Not started', failed: 'Failed' }[state]
  return (
    <span
      role="img"
      aria-label={`Step ${number}: ${label}`}
      className={cx(
        'inline-flex size-7 shrink-0 items-center justify-center font-mono text-mono font-medium',
        state === 'done' && 'border border-ok text-ok',
        state === 'current' && 'border-2 border-ink text-ink',
        state === 'next' && 'border border-rule text-ink-3',
        state === 'failed' && 'border border-danger text-danger',
      )}
    >
      {state === 'done' ? <Check size={16} /> : state === 'failed' ? <X size={16} /> : number}
    </span>
  )
}
