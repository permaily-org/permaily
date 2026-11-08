import { useSearchParams } from 'react-router'
import { ButtonAnchor, ButtonLink, Headline, Legend, SectionHeading, TextLink, type Cell } from '../../components/ui/index.ts'
import { attentionCount, connections, formatCount, formerConnections, ruleColumns } from '../../data/sample.ts'
import { ConnectionsTable } from './ConnectionsTable.tsx'

const legend: Array<{ cell: Cell; label: string }> = [
  { cell: 'shared', label: 'Can read' },
  { cell: 'headers', label: 'Headers only' },
  { cell: 'withheld', label: 'Withheld' },
]

export function ConnectionsPage() {
  const [params] = useSearchParams()
  if (params.get('state') === 'empty' || connections.length === 0) return <NoConnections />

  const waiting = attentionCount('connections')

  return (
    <div className="px-14 pt-9 pb-14">
      <header className="flex items-center justify-between gap-6">
        <div>
          <Headline>
            {connections.length} {connections.length === 1 ? 'connection' : 'connections'}.
          </Headline>
          <p className="mt-2 text-lead text-ink-2">
            Each one started with nothing.
            {waiting > 0 && ` ${waiting} ${waiting === 1 ? 'needs' : 'need'} you.`}
          </p>
        </div>
        <ButtonLink to="/connections/new">Connect an agent</ButtonLink>
      </header>

      <div className="mt-4">
        <ConnectionsTable />
      </div>

      <div className="mt-5 flex flex-wrap items-center gap-x-5 gap-y-2">
        <Legend items={legend} />
        <p className="text-mono text-ink-2">Cells run in Overview's order, from {ruleColumns[0].name} to what's withheld from all</p>
      </div>

      {formerConnections.length > 0 && (
        <section aria-labelledby="former-heading" className="mt-6">
          <SectionHeading id="former-heading" title="No longer connected" />
          <ul>
            {formerConnections.map((former) => (
              <li key={former.id} className="flex items-center gap-4 border-b border-rule py-[11px]">
                <p className="flex min-w-0 flex-1 flex-wrap gap-x-4">
                  <span className="font-medium">
                    {former.name}, revoked on {former.revoked}
                  </span>
                  <span className="text-ink-2">It may still hold the {formatCount(former.held)} emails it read</span>
                </p>
                <TextLink to="/activity" aria-label={`See what ${former.name} read`}>
                  See what it read
                </TextLink>
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  )
}

function NoConnections() {
  return (
    <div className="px-14 pt-9 pb-14">
      <Headline>No apps or agents yet.</Headline>
      <p className="mt-3 max-w-[640px] text-lead text-ink-2">
        When something asks for your mail, it comes here first. You'll see exactly what it would get before you decide.
      </p>
      <div className="mt-6 flex flex-col items-start gap-3">
        <ButtonLink to="/connections/new">Connect Claude</ButtonLink>
        <ButtonAnchor variant="secondary" href="https://permaily.com/docs">
          See how it works
          <span className="sr-only"> (opens in a new tab)</span>
        </ButtonAnchor>
      </div>
    </div>
  )
}
