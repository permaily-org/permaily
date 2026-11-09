import type { ReactNode } from 'react'
import { cx } from '../../lib/styles.ts'

export type Share = 'shared' | 'soft'

/** Mono label for machine facts, such as a query or a file type. */
export function Tag({ children, className }: { children: ReactNode; className?: string }) {
  return (
    <span
      className={cx(
        'inline-flex items-center rounded-sm border border-rule px-[7px] py-[3px] font-mono text-mono text-ink',
        className,
      )}
    >
      {children}
    </span>
  )
}

/** Highlighted text. Only for content that is shared: bright means shared or new, soft means partly shared or already shared. */
export function Mark({
  children,
  tone = 'shared',
  size = 'md',
  className,
}: {
  children: ReactNode
  tone?: Share
  size?: 'md' | 'sm'
  className?: string
}) {
  return (
    <span
      className={cx(
        'inline-flex px-1.5 py-0.5',
        tone === 'soft'
          ? 'bg-highlight-soft text-on-highlight-soft'
          : 'bg-highlight text-on-highlight',
        size === 'sm' ? 'text-small' : 'text-body',
        className,
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
        tone === 'soft'
          ? 'bg-highlight-soft text-on-highlight-soft'
          : 'bg-highlight text-on-highlight',
      )}
    >
      {children}
    </span>
  )
}

/** A redaction bar. Only for withheld content. */
export function Bar({
  width,
  height = 12,
  label,
}: {
  width: number | string
  height?: number
  label?: string
}) {
  return (
    <span
      className="block shrink-0 bg-bar"
      style={{ width, height }}
      role={label ? 'img' : undefined}
      aria-label={label}
      aria-hidden={label ? undefined : true}
    />
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
export function Count({
  children,
  tone = 'shared',
}: {
  children: ReactNode
  tone?: 'shared' | 'soft' | 'bar'
}) {
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

/** A rule's effect at a glance: a full bar holds back the whole email, a bar inside a line removes a detail. */
export function EffectMark({ effect }: { effect: 'hold' | 'remove' }) {
  if (effect === 'hold') return <Bar width={14} height={14} />
  return (
    <span
      aria-hidden
      className="inline-flex h-3.5 w-3.5 shrink-0 flex-col justify-center gap-[2px]"
    >
      <span className="h-px w-full bg-ink-2" />
      <span className="h-1.5 w-2.5 bg-bar" />
      <span className="h-px w-full bg-ink-2" />
    </span>
  )
}
