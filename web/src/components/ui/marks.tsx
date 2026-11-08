import type { ReactNode } from 'react'
import { cx } from '../../lib/styles.ts'

export type Share = 'shared' | 'soft'
export type Cell = 'shared' | 'headers' | 'withheld'

/** Mono label for machine facts, such as pack names. */
export function Tag({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <span className={cx('inline-flex items-center rounded-sm border border-rule px-[7px] py-[3px] font-mono text-mono text-ink', className)}>
      {children}
    </span>
  )
}

/** Highlighted text. Only for content that is shared: bright means shared or new, soft means partly shared or already shared. */
export function Mark({ children, tone = 'shared', size = 'md' }: { children: ReactNode; tone?: Share; size?: 'md' | 'sm' }) {
  return (
    <span
      className={cx(
        'inline-flex px-1.5 py-0.5',
        tone === 'soft' ? 'bg-highlight-soft text-on-highlight-soft' : 'bg-highlight text-on-highlight',
        size === 'sm' ? 'text-small' : 'text-body',
      )}
    >
      {children}
    </span>
  )
}

/** Which parts of an email left, such as "subject, body". */
export function PartsChip({ children, tone = 'shared' }: { children: ReactNode; tone?: Share }) {
  return (
    <span
      className={cx(
        'inline-flex shrink-0 px-1.5 py-0.5 font-mono text-mono',
        tone === 'soft' ? 'bg-highlight-soft text-on-highlight-soft' : 'bg-highlight text-on-highlight',
      )}
    >
      {children}
    </span>
  )
}

/** A redaction bar. Only for withheld content. */
export function Bar({ width, height = 12, label }: { width: number | string; height?: number; label?: string }) {
  return (
    <span className="block shrink-0 bg-bar" style={{ width, height }} role={label ? 'img' : undefined} aria-label={label} aria-hidden={label ? undefined : true} />
  )
}

/** Initials of an app, agent or vendor. */
export function Tile({ initials, size = 'md' }: { initials: string; size?: 'md' | 'sm' }) {
  return (
    <span
      aria-hidden
      className={cx(
        'inline-flex shrink-0 items-center justify-center rounded-sm border border-ink bg-raised font-mono font-medium text-ink',
        size === 'sm' ? 'size-6 text-micro' : 'size-9 text-small',
      )}
    >
      {initials}
    </span>
  )
}

/** The number block inside a headline. Inherits the headline's type size. */
export function Count({ children, tone = 'shared' }: { children: ReactNode; tone?: 'shared' | 'soft' | 'bar' }) {
  return (
    <span
      className={cx(
        'inline-flex px-[0.14em]',
        tone === 'bar' && 'bg-bar text-on-bar',
        tone === 'soft' && 'bg-highlight-soft text-on-highlight-soft',
        tone === 'shared' && 'bg-highlight text-on-highlight',
      )}
    >
      {children}
    </span>
  )
}

const cellFill: Record<Cell, string> = { shared: 'bg-highlight', headers: 'bg-highlight-soft', withheld: 'bg-bar' }

/** Six small cells, one per Overview column, with the last always the rules withheld from all. */
export function VisibilityStrip({ cells, label }: { cells: Cell[]; label: string }) {
  return (
    <span role="img" aria-label={label} className="inline-flex gap-0.5">
      {cells.map((cell, i) => (
        <span key={i} className={cx('h-3.5 w-5', cellFill[cell])} />
      ))}
    </span>
  )
}

export function Legend({ items }: { items: Array<{ cell: Cell; label: string }> }) {
  return (
    <div className="flex items-center gap-5">
      {items.map(({ cell, label }) => (
        <span key={label} className="inline-flex items-center gap-1.5 text-mono text-ink-2">
          <span aria-hidden className={cx('size-3.5', cellFill[cell])} />
          {label}
        </span>
      ))}
    </div>
  )
}
