import { ChevronLeft } from 'lucide-react'
import type { ReactNode } from 'react'
import { NavLink, useParams } from 'react-router'
import {
  Button,
  ButtonLink,
  Count,
  Headline,
  Notice,
  SectionHeading,
  TermRow,
  TextLink,
  Tile,
} from '../../components/ui/index.ts'
import { restore, revoke, useRevoked } from '../../data/revoked.ts'
import {
  type Connection,
  connectionById,
  connectionsOf,
  formatCount,
  mailboxes,
  receipts,
  type Vendor,
  vendorById,
} from '../../data/sample.ts'
import { cx, focusRing, pagePadding } from '../../lib/styles.ts'
import { attentionFor, isHeadersOnly } from './ConnectionsAccess.ts'
import { VendorConnectionsTable } from './ConnectionsTable.tsx'
import { revokedAt } from './DetailData.ts'
import { DetailReceipts } from './DetailReceipts.tsx'
import { DetailRevokedNotice } from './DetailRevoked.tsx'
import { DetailRules } from './DetailRules.tsx'
import { DetailAccess, DetailRuns } from './DetailTerms.tsx'
import { QuerySection } from './QuerySection.tsx'

/**
 * A vendor's page. /connections/:vendorId opens on its overview: its connections and what applies to the
 * vendor as a whole. Each connection opens in its own tab, /connections/:vendorId/:connectionId.
 */
export function ConnectionDetailPage() {
  const { vendorId, connectionId } = useParams()
  const vendor = vendorById(vendorId)
  const siblings = vendor ? connectionsOf(vendor.id) : []
  if (!vendor || siblings.length === 0) return <NotFound />
  if (!connectionId) return <Overview vendor={vendor} siblings={siblings} />
  const connection = connectionById(`${vendor.id}/${connectionId}`)
  if (!connection) return <NotFound />
  return <Detail key={connection.id} vendor={vendor} connection={connection} siblings={siblings} />
}

/** What belongs to the vendor rather than to one connection: its connections, the mailboxes they reach, and revoking them all. */
function Overview({ vendor, siblings }: { vendor: Vendor; siblings: Connection[] }) {
  const revokedIds = useRevoked()
  const ids = siblings.map((s) => s.id)
  const allRevoked = ids.every((id) => revokedIds.has(id))
  const reached = mailboxes.filter((m) => m.usedBy.some((id) => ids.includes(id)))

  const revokeAll = (
    <Button
      variant={allRevoked ? 'secondary' : 'danger'}
      size="sm"
      onClick={() => (allRevoked ? restore(ids) : revoke(ids))}
    >
      {allRevoked ? 'Grant access again' : ids.length > 1 ? 'Revoke all' : 'Revoke'}
      <span className="sr-only"> {vendor.name}</span>
    </Button>
  )

  return (
    <VendorFrame vendor={vendor} siblings={siblings} action={revokeAll}>
      <Headline className="mt-6">
        {siblings.length} {siblings.length === 1 ? 'connection' : 'connections'}.
      </Headline>

      <div className="mt-6">
        <VendorConnectionsTable vendor={vendor} />
      </div>

      <section aria-labelledby="about-heading" className="mt-9 max-w-[560px]">
        <SectionHeading id="about-heading" title={`About ${vendor.name}`} />
        <TermRow label="Type">{vendor.kind}</TermRow>
        {reached.length > 0 && (
          <TermRow label={reached.length === 1 ? 'Mailbox' : 'Mailboxes'}>
            <span className="flex flex-col gap-0.5">
              {reached.map((m) => (
                <span key={m.id} className="font-mono text-mono break-all">
                  {m.address}
                </span>
              ))}
            </span>
          </TermRow>
        )}
      </section>
    </VendorFrame>
  )
}

/** One connection's tab: its query and output, what it has read, what it can do, the rules on it and the queries it ran. */
function Detail({
  vendor,
  connection: c,
  siblings,
}: {
  vendor: Vendor
  connection: Connection
  siblings: Connection[]
}) {
  const revokedIds = useRevoked()
  const revoked = revokedIds.has(c.id)
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

  const revokeButton = (
    <Button
      variant={revoked ? 'secondary' : 'danger'}
      size="sm"
      onClick={() => (revoked ? restore([c.id]) : revoke([c.id]))}
    >
      {revoked ? 'Grant access again' : 'Revoke'}
      <span className="sr-only"> {c.name}</span>
    </Button>
  )

  return (
    <VendorFrame vendor={vendor} siblings={siblings} action={revokeButton}>
      <Headline className="mt-6">
        {c.name} can read{' '}
        <Count tone={revoked ? 'bar' : headersOnly ? 'soft' : 'shared'}>
          {revoked ? 0 : formatCount(c.readCount)}
        </Count>{' '}
        {unit}. {read}
      </Headline>
      <p className="mt-2.5 font-mono text-mono text-ink-2">
        {revoked
          ? `revoked today at ${revokedAt}`
          : `last run ${c.lastUsed.replace(/^Today, /, 'today at ')}, ends ${c.ends}`}
      </p>

      {revoked ? (
        <DetailRevokedNotice
          vendor={vendor}
          connection={c}
          items={items}
          headersOnly={headersOnly}
        />
      ) : (
        <PendingNotice connection={c} />
      )}

      {/* One column until there's room for the terms beside the query. */}
      <div className="mt-6 grid grid-cols-1 gap-9 xl:grid-cols-[minmax(0,1fr)_340px] xl:gap-14">
        <div className="flex min-w-0 flex-col gap-9">
          <QuerySection connection={c} />
          <DetailReceipts connection={c} items={items} unit={unit} revoked={revoked} />
        </div>
        <div className="flex min-w-0 flex-col gap-6">
          <DetailAccess connection={c} revoked={revoked} />
          <DetailRules connection={c} revoked={revoked} />
          <DetailRuns connection={c} />
        </div>
      </div>
    </VendorFrame>
  )
}

/** The way back, the vendor and its tabs: Overview first, then one per connection. The action on the right acts on the open tab. */
function VendorFrame({
  vendor,
  siblings,
  action,
  children,
}: {
  vendor: Vendor
  siblings: Connection[]
  action: ReactNode
  children: ReactNode
}) {
  const revoked = useRevoked()
  const tab = (isActive: boolean, dim = false) =>
    cx(
      'shrink-0 whitespace-nowrap border-b-2 pb-2.5 text-body',
      isActive
        ? 'border-ink font-medium text-ink'
        : cx('border-transparent hover:text-ink', dim ? 'text-ink-3' : 'text-ink-2'),
      focusRing,
    )

  return (
    <div className={pagePadding}>
      <div className="flex items-center gap-1">
        <ChevronLeft aria-hidden size={14} className="text-ink-2" />
        <TextLink to="/connections">Connections</TextLink>
      </div>

      <div className="mt-4 flex items-center gap-2.5">
        <Tile initials={vendor.initials} />
        <div className="flex min-w-0 flex-col">
          <span className="text-title font-medium">{vendor.name}</span>
          <span className="font-mono text-micro text-ink-2">
            {vendor.domain}, {vendor.delivery}
          </span>
        </div>
      </div>

      {/* The tabs scroll sideways when they don't fit, and the action keeps its place. */}
      <div className="mt-5 flex items-end justify-between gap-4 border-b border-rule sm:gap-6">
        <nav
          aria-label={vendor.name}
          className="-mb-px flex min-w-0 gap-6 overflow-x-auto [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
        >
          <NavLink to={`/connections/${vendor.id}`} end className={({ isActive }) => tab(isActive)}>
            Overview
          </NavLink>
          {siblings.map((s) => (
            <NavLink
              key={s.id}
              to={`/connections/${s.id}`}
              className={({ isActive }) => tab(isActive, revoked.has(s.id))}
            >
              {s.name}
              {revoked.has(s.id) && <span className="sr-only">, revoked</span>}
            </NavLink>
          ))}
        </nav>
        <div className="shrink-0 pb-2">{action}</div>
      </div>

      {children}
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
    <div className={pagePadding}>
      <Headline size="display-s">We can't find that connection.</Headline>
      <div className="mt-6">
        <TextLink to="/connections">Back to connections</TextLink>
      </div>
    </div>
  )
}
