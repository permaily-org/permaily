import type { ReactNode } from 'react'
import { cx } from '../../lib/styles.ts'
import { Bar, Mark } from './marks.tsx'

type WithheldProps = { kind: 'withheld'; widths?: [number, number, number] }
type SharedProps = { kind: 'shared' | 'soft' | 'new'; date: string; sender: string; subject: string; trailing?: ReactNode; hover?: boolean }
type ReasonProps = { kind: 'blocked' | 'excluded'; date: string; sender: string; reason: string; barWidth?: number }

export type MailRowProps = WithheldProps | SharedProps | ReasonProps

const row = 'flex h-10 items-center gap-4 border-b border-rule'

/**
 * One email as a connection would see it. Withheld rows keep the shape of an email as black bars.
 * Shared rows mark the subject. New rows are bright and tagged, so they never rely on color alone.
 */
export function MailRow(props: MailRowProps) {
  if ('subject' in props) return <SharedRow {...props} />
  if ('reason' in props) return <ReasonRow {...props} />
  return <WithheldRow {...props} />
}

function WithheldRow({ widths }: WithheldProps) {
  const [a, b, c] = widths ?? [44, 92, 360]
  return (
    <div className={row}>
      <span className="sr-only">Withheld email</span>
      <span className="flex w-16 shrink-0">
        <Bar width={a} />
      </span>
      <span className="flex w-[124px] shrink-0">
        <Bar width={b} />
      </span>
      <span className="flex min-w-0 flex-1">
        <Bar width={`min(${c}px, 100%)`} />
      </span>
    </div>
  )
}

function Who({ date, sender }: { date: string; sender: string }) {
  return (
    <>
      <span className="w-16 shrink-0 font-mono text-mono text-ink-2">{date}</span>
      <span className="w-[124px] shrink-0 truncate font-medium">{sender}</span>
    </>
  )
}

function ReasonRow({ kind, date, sender, reason, barWidth }: ReasonProps) {
  return (
    <div className={row}>
      <Who date={date} sender={sender} />
      <Bar width={barWidth ?? 200} />
      <span className={cx('truncate font-mono text-mono', kind === 'blocked' ? 'text-danger' : 'text-ink-2')}>{reason}</span>
    </div>
  )
}

function SharedRow({ kind, date, sender, subject, trailing, hover }: SharedProps) {
  return (
    <div className={cx(row, hover && 'bg-sunken pr-2')}>
      <Who date={date} sender={sender} />
      <span className="flex min-w-0 flex-1 items-center gap-3">
        <Mark tone={kind === 'soft' ? 'soft' : 'shared'}>{subject}</Mark>
        {kind === 'new' && <span className="font-mono text-mono font-medium text-ink">new</span>}
      </span>
      {trailing}
    </div>
  )
}
