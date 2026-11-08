import { SectionHeading, TermRow, TextLink } from '../../components/ui/index.ts'
import type { Connection } from '../../data/sample.ts'
import { changeRoute, neverSummary, refusedRequests, revokedAt } from './DetailData.ts'

/** The terms of the connection, and what it asked for outside them. */
export function DetailTerms({ connection, revoked }: { connection: Connection; revoked: boolean }) {
  const refused = refusedRequests[connection.id] ?? []
  const change = revoked ? undefined : (
    <TextLink to={changeRoute(connection.id)} aria-label={`Change what ${connection.name} can do`}>
      Change
    </TextLink>
  )

  return (
    <div className="flex flex-col gap-6">
      <section aria-labelledby="terms-heading">
        <SectionHeading id="terms-heading" title={revoked ? 'What it could do' : 'What it can do'} action={change} />
        <TermRow label="Emails">{connection.ruleNames}</TermRow>
        <TermRow label="Parts">{connection.parts}</TermRow>
        <TermRow label="Actions">{connection.actions}</TermRow>
        <TermRow label="Time">{revoked ? `Ended today at ${revokedAt}` : connection.time}</TermRow>
        {!revoked && <TermRow label="Never">{neverSummary}</TermRow>}
      </section>

      <section aria-labelledby="refused-heading">
        <SectionHeading id="refused-heading" title="What it asked for and didn't get" />
        {refused.length === 0 ? (
          <p className="py-[11px] text-body text-ink-2">Nothing so far.</p>
        ) : (
          refused.map((request) => (
            <TermRow key={request.when + request.text} label={request.when}>
              {request.text}
            </TermRow>
          ))
        )}
      </section>
    </div>
  )
}
