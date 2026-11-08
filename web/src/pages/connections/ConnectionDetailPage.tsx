import { ChevronLeft } from 'lucide-react'
import { useState } from 'react'
import { useParams } from 'react-router'
import { Button, ButtonLink, Count, Headline, Notice, TextLink, Tile } from '../../components/ui/index.ts'
import { connectionById, formatCount, receipts, type Connection } from '../../data/sample.ts'
import { attentionFor, isHeadersOnly } from './ConnectionsAccess.ts'
import { revokedAt } from './DetailData.ts'
import { DetailReceipts } from './DetailReceipts.tsx'
import { DetailRevokedNotice } from './DetailRevoked.tsx'
import { DetailTerms } from './DetailTerms.tsx'

export function ConnectionDetailPage() {
  const { id } = useParams()
  const connection = connectionById(id)
  if (!connection) return <NotFound />
  return <Detail key={connection.id} connection={connection} />
}

function Detail({ connection: c }: { connection: Connection }) {
  const [revoked, setRevoked] = useState(false)
  const items = receipts.filter((r) => r.connectionId === c.id)
  const headersOnly = isHeadersOnly(c)
  const unit = headersOnly ? 'headers' : 'emails'

  const read = revoked
    ? c.hasRead > 0
      ? `It may still hold ${formatCount(c.hasRead)}.`
      : 'It never read any.'
    : c.hasRead > 0
      ? `It has read ${formatCount(c.hasRead)}.`
      : "It hasn't read any."

  return (
    <div className="px-14 pt-9 pb-14">
      <div className="flex items-start justify-between gap-6">
        <div className="flex items-center gap-2.5">
          <Tile initials={c.initials} />
          <div className="flex flex-col gap-0.5">
            <div className="flex items-center gap-1">
              <ChevronLeft aria-hidden size={14} className="text-ink-2" />
              <TextLink to="/connections" aria-label="Back to Connections">
                Connections
              </TextLink>
            </div>
            <p className="text-small text-ink-2">
              {c.connectedVia}, {revoked ? `access revoked today at ${revokedAt}` : `last used ${c.lastUsed.replace(/^Today, /, 'today at ')}`}
            </p>
          </div>
        </div>
        <Button variant={revoked ? 'secondary' : 'danger'} onClick={() => setRevoked(!revoked)}>
          {revoked ? 'Grant access again' : 'Revoke access'}
        </Button>
      </div>

      <Headline className="mt-4">
        {c.name} can read{' '}
        <Count tone={revoked ? 'bar' : headersOnly ? 'soft' : 'shared'}>{revoked ? 0 : formatCount(c.readCount)}</Count> {unit}. {read}
      </Headline>

      {revoked ? (
        <DetailRevokedNotice connection={c} items={items} headersOnly={headersOnly} />
      ) : (
        <>
          <div className="mt-4 flex flex-wrap items-center gap-x-3 gap-y-1">
            <span className="text-body">
              {c.kind === 'Agent' ? 'Searches inside' : 'Asks for'} {c.asksFor.toLowerCase()}
            </span>
            <span className="font-mono text-mono">access ends {c.ends}</span>
          </div>
          <PendingNotice connection={c} />
        </>
      )}

      <div className="mt-5 grid grid-cols-[minmax(0,1fr)_340px] gap-14">
        <DetailReceipts connection={c} items={items} unit={unit} revoked={revoked} />
        <DetailTerms connection={c} revoked={revoked} />
      </div>
    </div>
  )
}

/** A request or a stale grant from Overview, repeated where it applies. */
function PendingNotice({ connection }: { connection: Connection }) {
  const item = attentionFor(connection)
  if (!item) return null
  const action =
    item.to === `/connections/${connection.id}` ? undefined : (
      <ButtonLink to={item.to} variant="secondary" size="sm">
        {item.action}
      </ButtonLink>
    )
  return (
    <div className="mt-4">
      <Notice title={item.title} action={action}>
        {item.detail}
      </Notice>
    </div>
  )
}

function NotFound() {
  return (
    <div className="px-14 pt-9 pb-14">
      <Headline size="display-s">We can't find that connection.</Headline>
      <p className="mt-3 text-lead text-ink-2">It may have been removed, or the link may be wrong.</p>
      <div className="mt-6">
        <TextLink to="/connections">Back to connections</TextLink>
      </div>
    </div>
  )
}
