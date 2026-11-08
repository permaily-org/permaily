import type { ReactNode } from 'react'
import { cx } from '../../lib/styles.ts'
import { Bar, Mark } from './marks.tsx'

type WithheldProps = { kind: 'withheld'; widths?: [number, number, number] }
type SharedProps = { kind: 'shared' | 'soft' | 'new'; date: string; sender: string; subject: string; trailing?: ReactNode; hover?: boolean }
type ReasonProps = { kind: 'blocked' | 'excluded' | 'held'; date: string; sender: string; reason: string; barWidth?: number; trailing?: ReactNode }
/** A line of an email with details taken out. Square brackets in the snippet mark what was removed. */
type TrimmedProps = { kind: 'trimmed'; date: string; sender: string; snippet: string; trailing?: ReactNode }

export type MailRowProps = WithheldProps | SharedProps | ReasonProps | TrimmedProps

const row = 'flex h-10 items-center gap-4 border-b border-rule'

/**
 * One email as a connection would see it. Withheld rows keep the shape of an email as black bars.
 * Shared rows mark the subject. New rows are bright and tagged, so they never rely on color alone.
 */
export function MailRow(props: MailRowProps) {
  if ('subject' in props) return <SharedRow {...props} />
  if ('reason' in props) return <ReasonRow {...props} />
  if ('snippet' in props) return <TrimmedRow {...props} />
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

/** Blocked is a request turned down. Held is mail a rule holds back on purpose. Excluded is mail left out of a rule. */
const reasonTone: Record<ReasonProps['kind'], string> = { blocked: 'text-danger', held: 'text-ink', excluded: 'text-ink-2' }

function ReasonRow({ kind, date, sender, reason, barWidth, trailing }: ReasonProps) {
  return (
    <div className={row}>
      <Who date={date} sender={sender} />
      <Bar width={`min(${barWidth ?? 200}px, 30%)`} />
      <span className={cx('min-w-0 flex-1 truncate font-mono text-mono', reasonTone[kind])}>{reason}</span>
      {trailing}
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

function TrimmedRow({ date, sender, snippet, trailing }: TrimmedProps) {
  const parts = snippet.split(/(\[[^\]]+\])/).filter(Boolean)
  return (
    <div className={row}>
      <Who date={date} sender={sender} />
      <span className="flex min-w-0 flex-1 items-center overflow-hidden whitespace-pre text-body">
        {parts.map((part, i) =>
          part.startsWith('[') ? (
            <span key={i} className="inline-flex">
              <Bar width={Math.min(200, (part.length - 2) * 7)} />
              <span className="sr-only">(removed)</span>
            </span>
          ) : (
            <span key={i} className="truncate">
              {part}
            </span>
          ),
        )}
      </span>
      {trailing}
    </div>
  )
}
