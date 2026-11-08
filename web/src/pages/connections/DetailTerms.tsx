import { SectionHeading, TermRow, TextLink } from '../../components/ui/index.ts'
import type { Connection } from '../../data/sample.ts'
import { cx } from '../../lib/styles.ts'
import { changeRoute, revokedAt } from './DetailData.ts'

/** What the connection asked for and was approved, and the queries it has run. Rules never appear here. */
export function DetailTerms({ connection, revoked }: { connection: Connection; revoked: boolean }) {
  const route = changeRoute(connection.id)
  const agent = connection.kind === 'Agent'
  const change =
    revoked || !route ? undefined : (
      <TextLink to={route} aria-label={`Change what ${connection.name} can do`}>
        Change
      </TextLink>
    )

  return (
    <div className="flex flex-col gap-6">
      <section aria-labelledby="terms-heading">
        <SectionHeading id="terms-heading" title={revoked ? 'What it could do' : 'What it can do'} action={change} />
        <TermRow label={agent ? 'Can search' : 'Asks for'}>{connection.asksFor}</TermRow>
        <TermRow label="Query">
          <span className={agent ? 'text-body' : 'font-mono text-mono break-all'}>{connection.query}</span>
        </TermRow>
        <TermRow label="Parts">{connection.parts}</TermRow>
        <TermRow label="Actions">{connection.actions}</TermRow>
        <TermRow label="Time">{revoked ? `Ended today at ${revokedAt}` : connection.time}</TermRow>
      </section>

      <section aria-labelledby="runs-heading">
        <SectionHeading id="runs-heading" title="Queries it ran" />
        {connection.runs.length === 0 ? (
          <p className="py-[11px] text-body text-ink-2">{connection.unused ? `Nothing in the last ${connection.lastUsed.replace(' ago', '')}.` : 'Nothing so far.'}</p>
        ) : (
          connection.runs.map((run) => (
            <div key={run.when + run.query} className="flex items-start gap-4 border-b border-rule py-[11px]">
              <span className="w-[42px] shrink-0 text-small text-ink-2">{run.when}</span>
              <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                <span className={agent ? 'font-mono text-mono' : 'text-body'}>{run.query}</span>
                <span className={cx('text-small', run.refused ? 'text-danger' : 'text-ink-2')}>{run.result}</span>
              </span>
            </div>
          ))
        )}
      </section>
    </div>
  )
}
