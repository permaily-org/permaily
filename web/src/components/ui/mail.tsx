import type { ReactNode } from 'react'
import { cx } from '../../lib/styles.ts'
import { Bar, Mark } from './marks.tsx'

type WithheldProps = { kind: 'withheld'; widths?: [number, number, number] }
type SharedProps = {
  kind: 'shared' | 'soft' | 'new'
  date: string
  sender: string
  subject: string
  trailing?: ReactNode
  hover?: boolean
}
type ReasonProps = {
  kind: 'blocked' | 'excluded' | 'held'
  date: string
  sender: string
  reason: string
  barWidth?: number
  trailing?: ReactNode
}
/** A line of an email with details taken out. Square brackets in the snippet mark what was removed. */
type TrimmedProps = {
  kind: 'trimmed'
  date: string
  sender: string
  snippet: string
  trailing?: ReactNode
}

export type MailRowProps = WithheldProps | SharedProps | ReasonProps | TrimmedProps

/**
 * Each row goes by its own width, not the window's: one 40px line from 512px. Narrower, as on a phone or in the rule
 * editor's side pane, the date, sender and any action share the first line, and the subject, reason or snippet gets
 * the second, full width. Grid placement has no effect once the row is a flex line.
 */
const row = '@container'
// The rule is part of the line, so a single line stays 40px tall with it.
const line =
  'grid grid-cols-[4rem_minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 border-b border-rule py-2 @lg:flex @lg:h-10 @lg:gap-4 @lg:py-0'

/** The subject, reason or snippet: the second line of a narrow row. */
const body = 'col-span-3 row-start-2'

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
      <div className={line}>
        <span className="sr-only">Withheld email</span>
        <span className="flex w-16 shrink-0">
          <Bar width={a} />
        </span>
        <span className="flex w-[124px] shrink-0">
          <Bar width={b} />
        </span>
        <span className={cx('flex min-w-0 flex-1', body)}>
          <Bar width={`min(${c}px, 100%)`} />
        </span>
      </div>
    </div>
  )
}

function Who({ date, sender }: { date: string; sender: string }) {
  return (
    <>
      <span className="w-16 shrink-0 font-mono text-mono text-ink-2">{date}</span>
      <span className="min-w-0 truncate font-medium @lg:w-[124px] @lg:shrink-0">{sender}</span>
    </>
  )
}

/** Any action on the row sits at the end of its first line. On a single line the wrapper steps aside, so the action is part of it. */
function Trailing({ children }: { children: ReactNode }) {
  if (!children) return null
  return (
    <span className="col-start-3 row-start-1 flex shrink-0 items-center @lg:contents">
      {children}
    </span>
  )
}

/** Blocked is a request turned down. Held is mail a rule holds back on purpose. Excluded is mail left out of a rule. */
const reasonTone: Record<ReasonProps['kind'], string> = {
  blocked: 'text-danger',
  held: 'text-ink',
  excluded: 'text-ink-2',
}

function ReasonRow({ kind, date, sender, reason, barWidth, trailing }: ReasonProps) {
  return (
    <div className={row}>
      <div className={line}>
        <Who date={date} sender={sender} />
        {/* On a single line the wrapper steps aside, so the bar and the reason sit in the line as before. */}
        <span className={cx('flex min-w-0 items-center gap-3 @lg:contents', body)}>
          <Bar width={`min(${barWidth ?? 200}px, 30%)`} />
          <span className={cx('min-w-0 flex-1 truncate font-mono text-mono', reasonTone[kind])}>
            {reason}
          </span>
        </span>
        <Trailing>{trailing}</Trailing>
      </div>
    </div>
  )
}

function SharedRow({ kind, date, sender, subject, trailing, hover }: SharedProps) {
  return (
    <div className={cx(row, hover && 'bg-sunken pr-2')}>
      <div className={line}>
        <Who date={date} sender={sender} />
        <span className={cx('flex min-w-0 flex-1 items-center gap-3', body)}>
          {/* A long subject ends in an ellipsis rather than wrapping out of its row. */}
          <Mark tone={kind === 'soft' ? 'soft' : 'shared'} className="min-w-0 max-w-full">
            <span className="truncate">{subject}</span>
          </Mark>
          {kind === 'new' && (
            <span className="shrink-0 font-mono text-mono font-medium text-ink">new</span>
          )}
        </span>
        <Trailing>{trailing}</Trailing>
      </div>
    </div>
  )
}

function TrimmedRow({ date, sender, snippet, trailing }: TrimmedProps) {
  const parts = snippet.split(/(\[[^\]]+\])/).filter(Boolean)
  return (
    <div className={row}>
      <div className={line}>
        <Who date={date} sender={sender} />
        <span
          className={cx(
            'flex min-w-0 flex-1 items-center overflow-hidden whitespace-pre text-body',
            body,
          )}
        >
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
        <Trailing>{trailing}</Trailing>
      </div>
    </div>
  )
}
