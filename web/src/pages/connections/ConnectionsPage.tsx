import { useState } from 'react'
import { useSearchParams } from 'react-router'
import {
  ButtonAnchor,
  ButtonLink,
  Headline,
  SearchField,
  SectionHeading,
  TextLink,
} from '../../components/ui/index.ts'
import { connections, formatCount, formerVendors } from '../../data/sample.ts'
import { pagePadding } from '../../lib/styles.ts'
import { ConnectionsTable } from './ConnectionsTable.tsx'

export function ConnectionsPage() {
  const [params] = useSearchParams()
  const [search, setSearch] = useState('')
  if (params.get('state') === 'empty' || connections.length === 0) return <NoConnections />

  return (
    <div className={pagePadding}>
      <header className="flex flex-wrap items-center justify-between gap-x-6 gap-y-4">
        <Headline>
          {connections.length} {connections.length === 1 ? 'connection' : 'connections'}.
        </Headline>
        <ButtonLink to="/connections/new">Connect an agent</ButtonLink>
      </header>

      <SearchField
        label="Find a connection or query"
        placeholder="Find a connection or query"
        value={search}
        onChange={(event) => setSearch(event.target.value)}
        className="mt-6 w-full max-w-[280px]"
      />

      <div className="mt-5">
        <ConnectionsTable search={search} />
      </div>

      {formerVendors.length > 0 && !search.trim() && (
        <section aria-labelledby="former-heading" className="mt-9">
          <SectionHeading id="former-heading" title="No longer connected" />
          <ul>
            {formerVendors.map((former) => (
              <li
                key={former.id}
                className="flex flex-wrap items-center gap-x-4 gap-y-1 border-b border-rule py-[11px]"
              >
                <span className="font-medium">{former.name}</span>
                <span className="font-mono text-mono text-ink-2">revoked {former.revoked}</span>
                <span className="min-w-0 flex-1 text-small text-ink-2">
                  May still hold {formatCount(former.held)} emails
                </span>
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
    <div className={pagePadding}>
      <Headline>No connections yet.</Headline>
      <div className="mt-6 flex flex-wrap items-center gap-3">
        <ButtonLink to="/connections/new">Connect Claude</ButtonLink>
        <ButtonAnchor variant="secondary" href="https://permaily.com/docs">
          How it works
          <span className="sr-only"> (opens in a new tab)</span>
        </ButtonAnchor>
      </div>
    </div>
  )
}
