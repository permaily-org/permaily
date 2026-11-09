import { SectionHeading, TermRow, TextLink } from '../../components/ui/index.ts'
import type { Connection } from '../../data/sample.ts'
import { cx } from '../../lib/styles.ts'
import { isOwnSearches } from './ConnectionsAccess.ts'
import { changeRoute, revokedAt } from './DetailData.ts'
import { Runs } from './Runs.tsx'

/** What the connection may do besides its query: when it runs, its webhook, parts, actions and time. */
export function DetailAccess({
  connection: c,
  revoked,
}: {
  connection: Connection
  revoked: boolean
}) {
  const route = changeRoute(c.id)
  const change =
    revoked || !route ? undefined : (
      <TextLink to={route} aria-label={`Change what ${c.name} can do`}>
        Change
      </TextLink>
    )

  return (
    <section aria-labelledby="terms-heading">
      <SectionHeading
        id="terms-heading"
        title={revoked ? 'What it could do' : 'What it can do'}
        action={change}
      />
      <TermRow label="Runs">
        <Runs trigger={c.trigger} long />
      </TermRow>
      {c.webhook && (
        <TermRow label="Webhook">
          <span className="flex min-w-0 flex-col gap-0.5">
            <span
              className={cx('font-mono text-mono break-all', revoked && 'text-ink-2 line-through')}
            >
              {c.webhook.url}
            </span>
            <span className="text-small text-ink-2">
              {revoked
                ? 'No longer called'
                : `Last call ${c.webhook.lastCall.replace(/^Today, /, 'today at ')}`}
            </span>
          </span>
        </TermRow>
      )}
      <TermRow label="Parts">{c.parts}</TermRow>
      <TermRow label="Actions">{c.actions}</TermRow>
      <TermRow label="Time">{revoked ? `Ended today at ${revokedAt}` : c.time}</TermRow>
    </section>
  )
}

/** Each run of its query, newest first, with how many emails came back and no reason for an empty result. */
export function DetailRuns({ connection: c }: { connection: Connection }) {
  const own = isOwnSearches(c)
  return (
    <section aria-labelledby="runs-heading">
      <SectionHeading id="runs-heading" title="Queries it ran" />
      {c.runs.length === 0 ? (
        <p className="py-[11px] text-body text-ink-2">
          {c.unused ? `None in ${c.lastUsed.replace(' ago', '')}` : 'None yet'}
        </p>
      ) : (
        c.runs.map((run) => (
          <div
            key={run.when + run.query}
            className="flex items-start gap-4 border-b border-rule py-[11px]"
          >
            <span className="w-[42px] shrink-0 text-small text-ink-2">{run.when}</span>
            <span className="flex min-w-0 flex-1 flex-col gap-0.5">
              <span className={own ? 'font-mono text-mono' : 'text-body'}>{run.query}</span>
              <span className={cx('text-small', run.refused ? 'text-danger' : 'text-ink-2')}>
                {run.result}
              </span>
            </span>
          </div>
        ))
      )}
    </section>
  )
}
