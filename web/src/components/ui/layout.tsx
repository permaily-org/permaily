import type { ReactNode } from 'react'
import { cx } from '../../lib/styles.ts'

/** A fact-stating headline. Put a <Count> inside for the number. */
export function Headline({
  children,
  size = 'display',
  as: Tag = 'h1',
  className,
}: {
  children: ReactNode
  size?: 'display-l' | 'display' | 'display-s' | 'heading'
  as?: 'h1' | 'h2' | 'p'
  className?: string
}) {
  const sizes = { 'display-l': 'text-display-l', display: 'text-display', 'display-s': 'text-display-s', heading: 'text-heading' }
  return <Tag className={cx('flex flex-wrap items-center gap-x-2.5 gap-y-1 font-medium', sizes[size], className)}>{children}</Tag>
}

export function SectionHeading({ title, action, id }: { title: ReactNode; action?: ReactNode; id?: string }) {
  return (
    <div className="flex items-center justify-between gap-4 border-b border-rule-strong pb-2.5">
      <h2 id={id} className="text-title font-medium">
        {title}
      </h2>
      {action}
    </div>
  )
}

/** One labelled row in a list of terms, such as Parts or Time. */
export function TermRow({ label, children, action }: { label: ReactNode; children: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex items-center gap-4 border-b border-rule py-[11px]">
      <div className="w-[84px] shrink-0 text-small text-ink-2">{label}</div>
      <div className="flex min-w-0 flex-1 items-center justify-between gap-2.5">
        <div className="min-w-0 text-body">{children}</div>
        {action}
      </div>
    </div>
  )
}

/** Day label above a group of rows, such as TODAY. */
export function DayHeading({ children }: { children: ReactNode }) {
  return <div className="pt-3.5 pb-2 font-mono text-micro uppercase text-ink-2">{children}</div>
}

export function Notice({
  tone = 'attention',
  title,
  children,
  action,
}: {
  tone?: 'attention' | 'neutral' | 'alarm'
  title: ReactNode
  children?: ReactNode
  action?: ReactNode
}) {
  return (
    <div
      role={tone === 'alarm' ? 'alert' : 'status'}
      className={cx(
        'flex items-center gap-4 rounded-sm px-4 py-3',
        tone === 'attention' && 'bg-attention-bg text-attention',
        tone === 'neutral' && 'border border-ink text-ink',
        tone === 'alarm' && 'bg-danger text-on-danger',
      )}
    >
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <p className="font-medium">{title}</p>
        {children && <div className={cx('text-small', tone === 'neutral' && 'text-ink-2')}>{children}</div>}
      </div>
      {action}
    </div>
  )
}
