import { ChevronDown, ChevronRight } from 'lucide-react'
import { type ReactNode, useSyncExternalStore } from 'react'
import { Link } from 'react-router'
import { ButtonLink, Mark, TextAction, Tile } from '../../components/ui/index.ts'
import { restore, revoke, useRevoked } from '../../data/revoked.ts'
import {
  type Connection,
  connectionLabel,
  connectionsOf,
  type Vendor,
  vendors,
} from '../../data/sample.ts'
import { createStore } from '../../data/store.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { attentionFor, isHeadersOnly } from './ConnectionsAccess.ts'
import { Runs } from './Runs.tsx'

/**
 * Columns that need room, by the width of the table itself, so it fits a phone as well as the space beside an
 * expanded sidebar: the query from 576px, and when it runs and last ran from 896px.
 */
const queryColumn = '@max-xl:hidden'
const wideColumn = '@max-4xl:hidden'

/** Quiet until the row is reached with the pointer or the keyboard. */
const onReach =
  'opacity-0 group-hover:opacity-100 group-focus-within:opacity-100 focus-visible:opacity-100'

/** Vendors whose connections are hidden. Kept for the visit, so they stay hidden after opening a connection and coming back. */
const collapsed = createStore<ReadonlySet<string>>(new Set())

function toggleCollapsed(vendorId: string): void {
  const next = new Set(collapsed.get())
  if (!next.delete(vendorId)) next.add(vendorId)
  collapsed.set(next)
}

function matches(vendor: Vendor, connection: Connection, query: string): boolean {
  if (!query) return true
  return [vendor.name, vendor.domain, connection.name, connection.asksFor, connection.query].some(
    (field) => field.toLowerCase().includes(query),
  )
}

/** Each vendor with its connections underneath, which can be hidden. A search narrows both and shows every match. */
export function ConnectionsTable({ search }: { search: string }) {
  const revoked = useRevoked()
  const hidden = useSyncExternalStore(collapsed.subscribe, collapsed.get)
  const query = search.trim().toLowerCase()
  const groups = vendors
    .map((vendor) => ({
      vendor,
      items: connectionsOf(vendor.id).filter((c) => matches(vendor, c, query)),
    }))
    .filter((group) => group.items.length > 0)

  if (groups.length === 0)
    return <p className="py-6 text-body text-ink-2">No connection matches “{search.trim()}”.</p>

  return (
    <TableFrame label="Connections by vendor">
      {groups.map(({ vendor, items }) => {
        // While a search is typed every match shows, so the toggle steps aside.
        const open = query !== '' || !hidden.has(vendor.id)
        return (
          <tbody key={vendor.id}>
            <VendorRow
              vendor={vendor}
              revoked={revoked}
              open={open}
              onToggle={query ? undefined : () => toggleCollapsed(vendor.id)}
            />
            {open &&
              items.map((connection) => (
                <ConnectionRow
                  key={connection.id}
                  connection={connection}
                  revoked={revoked.has(connection.id)}
                  nested
                />
              ))}
          </tbody>
        )
      })}
    </TableFrame>
  )
}

/** One vendor's connections, on its own page. Each row opens that connection's tab. */
export function VendorConnectionsTable({ vendor }: { vendor: Vendor }) {
  const revoked = useRevoked()
  return (
    <TableFrame label={`${vendor.name}'s connections`}>
      <tbody>
        {connectionsOf(vendor.id).map((connection) => (
          <ConnectionRow
            key={connection.id}
            connection={connection}
            revoked={revoked.has(connection.id)}
          />
        ))}
      </tbody>
    </TableFrame>
  )
}

/** The columns and their headings, shared by the list of every vendor and a vendor's own page. */
function TableFrame({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="@container">
      {/* The header cells set the column widths. A hidden column hides in every row, so the rest stay aligned. */}
      <table aria-label={label} className="w-full table-fixed border-collapse">
        <thead>
          <tr className="border-b border-rule-strong text-left text-small text-ink-2">
            <th scope="col" className="pb-2 font-normal @xl:w-[200px] @4xl:w-[248px]">
              Connection
            </th>
            <th scope="col" className={cx('pb-2 font-normal', queryColumn)}>
              Query
            </th>
            <th scope="col" className={cx('w-[152px] pb-2 font-normal', wideColumn)}>
              Runs
            </th>
            <th scope="col" className="w-[120px] pb-2 font-normal @xl:w-[132px]">
              Can read
            </th>
            <th scope="col" className={cx('w-[116px] pb-2 font-normal', wideColumn)}>
              Last run
            </th>
            <th scope="col" className="w-[84px] pb-2">
              <span className="sr-only">Action</span>
            </th>
          </tr>
        </thead>
        {children}
      </table>
    </div>
  )
}

/** The whole row opens the vendor's page. The chevron in the margin hides or shows its connections. */
function VendorRow({
  vendor,
  revoked,
  open,
  onToggle,
}: {
  vendor: Vendor
  revoked: ReadonlySet<string>
  open: boolean
  onToggle?: () => void
}) {
  const ids = connectionsOf(vendor.id).map((c) => c.id)
  const allRevoked = ids.every((id) => revoked.has(id))
  // A hidden vendor closes with a rule, like the last of its connections would.
  const cell = open ? 'pt-4 pb-1.5' : 'py-4'
  return (
    <tr className={cx('group relative hover:bg-sunken', !open && 'border-b border-rule')}>
      {/* One cell per column, so the row lines up whichever columns show. The name may run into the empty cells beside it. */}
      <th scope="rowgroup" className={cx(cell, 'text-left font-normal')}>
        <span className="flex items-center gap-2.5 whitespace-nowrap">
          {onToggle && (
            // In the page's margin from sm. On phones the margin is too narrow, so it sits in place of the tile.
            <button
              type="button"
              onClick={onToggle}
              aria-expanded={open}
              aria-label={`${vendor.name}'s connections`}
              className={cx(
                'relative z-10 inline-flex h-6 w-5 shrink-0 items-center justify-center rounded-sm text-ink-2 hover:text-ink sm:-ml-[30px]',
                focusRing,
              )}
            >
              {open ? (
                <ChevronDown aria-hidden size={16} />
              ) : (
                <ChevronRight aria-hidden size={16} />
              )}
            </button>
          )}
          <span className="max-sm:hidden">
            <Tile initials={vendor.initials} />
          </span>
          <span className="flex min-w-0 flex-col">
            <Link
              to={`/connections/${vendor.id}`}
              className={cx('text-body font-medium after:absolute after:inset-0', focusRing)}
            >
              {vendor.name}
            </Link>
            <span className="font-mono text-micro text-ink-2">
              {vendor.domain}, {vendor.delivery}
            </span>
          </span>
        </span>
      </th>
      <td className={cx(cell, queryColumn)} />
      <td className={cx(cell, wideColumn)} />
      <td className={cell} />
      <td className={cx(cell, wideColumn)} />
      <td className={cx(cell, 'text-right')}>
        {ids.length > 1 &&
          (allRevoked ? (
            <TextAction
              className="relative z-10"
              onClick={() => restore(ids)}
              aria-label={`Undo revoking ${vendor.name}`}
            >
              Undo
            </TextAction>
          ) : (
            <TextAction
              tone="danger"
              className={cx('relative z-10', onReach)}
              onClick={() => revoke(ids)}
              aria-label={`Revoke all of ${vendor.name}'s connections`}
            >
              Revoke all
            </TextAction>
          ))}
      </td>
    </tr>
  )
}

/** A nested row sits under its vendor's name, in the list of every vendor. */
function ConnectionRow({
  connection: c,
  revoked,
  nested = false,
}: {
  connection: Connection
  revoked: boolean
  nested?: boolean
}) {
  return (
    <tr className="group relative border-b border-rule hover:bg-sunken">
      <td className={cx('py-2.5 pr-4', nested && 'pl-[30px] sm:pl-[46px]')}>
        <Link
          to={`/connections/${c.id}`}
          aria-label={connectionLabel(c)}
          className={cx(
            'block truncate text-body font-medium after:absolute after:inset-0',
            revoked && 'text-ink-2',
            focusRing,
          )}
        >
          {c.name}
        </Link>
        {/* Without the query column, what it asks for sits under the name. */}
        <span className="block truncate text-small text-ink-2 @xl:hidden">{c.asksFor}</span>
        {revoked ? (
          <span className="block text-small text-ink-2">Revoked</span>
        ) : (
          c.flag && <span className="block truncate text-small text-attention">{c.flag}</span>
        )}
      </td>
      <td className={cx('py-2.5 pr-4', queryColumn)}>
        <p className="truncate text-body">{c.asksFor}</p>
        <p className="truncate font-mono text-micro text-ink-2">{c.query}</p>
      </td>
      <td className={cx('py-2.5 pr-4 text-small', wideColumn)}>
        <Runs trigger={c.trigger} />
      </td>
      <td className="py-2.5 pr-4 whitespace-nowrap">
        {revoked ? (
          <span className="inline-flex bg-bar px-1.5 py-0.5 text-small text-on-bar">
            0 {isHeadersOnly(c) ? 'headers' : 'emails'}
          </span>
        ) : (
          <Mark tone={isHeadersOnly(c) ? 'soft' : 'shared'} size="sm">
            {c.readLabel}
          </Mark>
        )}
      </td>
      <td
        className={cx(
          'py-2.5 pr-4 font-mono text-mono',
          wideColumn,
          c.unused && !revoked ? 'text-attention' : 'text-ink',
        )}
      >
        {c.lastUsedShort}
      </td>
      <td className="py-2.5 text-right">
        <RowAction connection={c} revoked={revoked} />
      </td>
    </tr>
  )
}

/** A request gets its button. Otherwise the row offers Revoke when reached, and Undo after. */
function RowAction({ connection, revoked }: { connection: Connection; revoked: boolean }) {
  const label = connectionLabel(connection)
  const item = attentionFor(connection)

  if (revoked) {
    return (
      <TextAction
        className="relative z-10"
        onClick={() => restore([connection.id])}
        aria-label={`Undo revoking ${label}`}
      >
        Undo
      </TextAction>
    )
  }
  if (item?.kind === 'decision') {
    return (
      <ButtonLink
        to={item.to}
        variant="secondary"
        size="sm"
        aria-label={`${item.action} ${label}`}
        className="relative z-10"
      >
        {item.action}
      </ButtonLink>
    )
  }
  const revokeButton = (className: string) => (
    <TextAction
      tone="danger"
      className={cx('z-10', className)}
      onClick={() => revoke([connection.id])}
      aria-label={`Revoke ${label}`}
    >
      Revoke
    </TextAction>
  )
  // A stale grant keeps its Revoke in view. Every other row shows it when reached.
  if (item) return revokeButton('relative')
  return (
    <span className="relative inline-flex h-5 items-center justify-end">
      <ChevronRight
        aria-hidden
        size={16}
        className="text-ink-2 group-focus-within:opacity-0 group-hover:opacity-0"
      />
      {revokeButton(cx('absolute right-0', onReach))}
    </span>
  )
}
