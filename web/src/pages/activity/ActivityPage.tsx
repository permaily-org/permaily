import {
  type ReactNode,
  type RefObject,
  useEffect,
  useId,
  useMemo,
  useRef,
  useState,
  useSyncExternalStore,
} from 'react'
import { useSearchParams } from 'react-router'
import {
  Button,
  DayHeading,
  Headline,
  InfoTip,
  PartsChip,
  Tag,
  TextAction,
  TextLink,
} from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'
import { cx, focusRing, pagePadding } from '../../lib/styles.ts'
import { useStanding } from '../accounts/standing.ts'
import { messageFor } from '../auth/messages.ts'
import { endWords } from '../connections/words.tsx'
import { type Known, ReceiptPanel } from './ReceiptPanel.tsx'
import {
  type Failure,
  type Filter,
  filtering,
  type KindFilter,
  kindFilters,
  matches,
  type Row,
  rowTime,
  useRecord,
} from './record.ts'
import {
  breakWords,
  clockWords,
  consentWords,
  count,
  dayWords,
  endedDevice,
  givenWords,
  linkKept,
  linkKindChip,
  linkVerdictChip,
  linkVerdictWords,
  receiptVerdictChip,
  receiptVerdictWords,
  refusedWords,
  senderWords,
  shortDigest,
} from './words.ts'

/*
  The Activity screen (ADR 0041, "The activity screen"): the account's record of what left its mailboxes, read from the
  walk of `GET /api/v1/receipts`, the newest first, with every break of the chain a row of its own in every filter. It
  stores nothing: the one thing in the address is `?connection=`, a connection's identifier, already on its own page.
  Words are a first draft for the designer's review.
*/

const xl = '(min-width: 80rem)'

function subscribeToXl(onChange: () => void) {
  const query = window.matchMedia(xl)
  query.addEventListener('change', onChange)
  return () => query.removeEventListener('change', onChange)
}

/** From xl the receipt has its own column beside the list. Below it, a receipt opens under its row. */
function useWide(): boolean {
  return useSyncExternalStore(subscribeToXl, () => window.matchMedia(xl).matches)
}

/** The connections and mailboxes listed now, for names: what the screen shows without them is the receipt's own. */
function useKnown(closing: boolean): Known {
  const { failed } = useSession()
  const [connections, setConnections] = useState<ReadonlyMap<string, api.Connection>>(new Map())
  const [mailboxes, setMailboxes] = useState<ReadonlyMap<string, api.Mailbox> | null>(null)
  useEffect(() => {
    let live = true
    // A closing account's connections have ended with it; the receipts name their vendor.
    if (!closing)
      api.connections().then(
        ({ connections: list }) => {
          if (live) setConnections(new Map(list.map((c) => [c.id, c])))
        },
        (error) => failed(error),
      )
    api.mailboxes().then(
      ({ mailboxes: list }) => {
        if (live) setMailboxes(new Map(list.map((m) => [m.id, m])))
      },
      (error) => failed(error),
    )
    return () => {
      live = false
    }
  }, [closing, failed])
  return { connections, mailboxes, linkConnections: !closing }
}

export function ActivityPage() {
  const [params, setParams] = useSearchParams()
  const connection = params.get('connection')
  const [kind, setKind] = useState<KindFilter>('all')
  const filter = useMemo<Filter>(() => ({ connection, kind }), [connection, kind])
  const standing = useStanding()
  const known = useKnown(standing.closing)
  const { record, loading, failure, press, restarted, read } = useRecord()
  const wide = useWide()
  const [pickedKey, setPickedKey] = useState<string | null>(null)
  const [said, setSaid] = useState('')
  const asked = useRef<'first' | 'more'>('first')
  const moreRef = useRef<HTMLButtonElement>(null)
  const endRef = useRef<HTMLParagraphElement>(null)
  const filterRef = useRef(filter)
  filterRef.current = filter

  // The first page, once, with the filter the page opened with: a later `read` (a new session callback) reads nothing
  // again, which would list the next pages twice.
  const started = useRef(false)
  useEffect(() => {
    if (started.current) return
    started.current = true
    void read(filterRef.current)
  }, [read])

  // After "Show older" a polite word of what it loaded, and focus where it was, or on the end once there is no more.
  useEffect(() => {
    if (!press || asked.current !== 'more') return
    setSaid(
      press.found > 0
        ? `Showed ${count(press.found)} more ${press.found === 1 ? 'receipt' : 'receipts'}`
        : 'No more',
    )
    if (record.done) endRef.current?.focus()
  }, [press, record.done])

  function more() {
    if (loading) return
    asked.current = 'more'
    setSaid('')
    void read(filter)
  }

  const shown = record.rows.filter((row) => matches(row, filter))
  const picked = shown.find((row) => row.key === pickedKey)
  const selected = wide ? (picked ?? shown[0]) : picked
  const head = record.head
  const empty = head !== null && head.n === 0

  // Opening a receipt closes the one before it, which can move the tapped row up and out of view, so keep it on screen.
  useEffect(() => {
    if (wide || !pickedKey) return
    for (const element of document.querySelectorAll<HTMLElement>('[data-row]'))
      if (element.dataset.row === pickedKey) element.scrollIntoView?.({ block: 'nearest' })
  }, [pickedKey, wide])

  return (
    <div className="flex min-h-svh flex-col xl:flex-row">
      <div className={cx('min-w-0 flex-1', pagePadding)}>
        <Headline>Activity</Headline>
        {head && head.n > 0 && <Head head={head} />}
        {empty && (
          <div className="mt-6">
            <p className="text-body">Nothing has left your mailboxes.</p>
            {!standing.closing && (
              <TextLink to="/connections" className="mt-3">
                Connections
              </TextLink>
            )}
          </div>
        )}
        {restarted && (
          <p className="mt-4 text-small text-ink-2">
            Your record changed while you read it, so it shows from the newest again.
          </p>
        )}

        {!empty && (
          <Filters
            filter={filter}
            known={known}
            rows={record.rows}
            onConnection={(id) =>
              setParams(id ? { connection: id } : {}, { replace: true, preventScrollReset: true })
            }
            onKind={setKind}
          />
        )}

        <div aria-busy={loading} className="mt-2">
          {loading && record.rows.length === 0 && (
            <p role="status" className="py-3 text-small text-ink-2">
              Reading your record…
            </p>
          )}
          {groupByDay(shown).map(({ day, rows }) => (
            <section key={`${day}:${rows[0]?.key}`} aria-label={day}>
              <DayHeading>{day}</DayHeading>
              <ul>
                {rows.map((row) => (
                  <RowItem
                    key={row.key}
                    row={row}
                    known={known}
                    selected={row.key === selected?.key}
                    inline={!wide}
                    // From xl a click picks the row. Below it a tap opens it under the row, and another tap closes it.
                    onSelect={() => setPickedKey(wide || row.key !== pickedKey ? row.key : null)}
                  />
                ))}
              </ul>
            </section>
          ))}
        </div>

        {failure && <FailureLine failure={failure} onRetry={() => void read(filter)} />}

        {head && !empty && !failure && (
          <More
            done={record.done}
            loading={loading}
            line={moreLine(
              filter,
              record.rows.length > 0 ? shown.length : 0,
              record.examined,
              press,
            )}
            onMore={more}
            moreRef={moreRef}
            endRef={endRef}
          />
        )}
        <p role="status" className="sr-only">
          {said}
        </p>
      </div>

      {wide && !empty && head && (
        <aside
          aria-label="Receipt"
          className="sticky top-0 h-svh w-[440px] shrink-0 overflow-y-auto border-l border-rule bg-raised px-9 pt-8 pb-10"
        >
          {selected ? (
            <ReceiptPanel row={selected} title={panelTitle(selected)} known={known} />
          ) : (
            <p className="text-body text-ink-2">Pick a row to see its receipt.</p>
          )}
        </aside>
      )}
    </div>
  )
}

/** The chain's head: how long the record is, and the newest link to note before a download for checking. */
function Head({ head }: { head: api.ReceiptsHead }) {
  const [copied, setCopied] = useState('')
  const kept = linkKept(head.verdict) && Boolean(head.sha256)
  async function copy() {
    try {
      await navigator.clipboard.writeText(`${head.n}:${head.sha256}`)
      setCopied('Copied.')
    } catch {
      setCopied("Couldn't copy. Select it and copy it yourself.")
    }
  }
  return (
    <div className="mt-3 flex flex-col gap-1.5">
      <p className="text-body">
        Your record has {count(head.n)} {head.n === 1 ? 'link' : 'links'}.
      </p>
      {kept ? (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
          <p className="text-small">
            Newest: link {count(head.n)},{' '}
            <span className="font-mono text-mono">sha256 {shortDigest(head.sha256 ?? '')}</span>
          </p>
          <TextAction size="sm" onClick={copy}>
            Copy <span className="sr-only">the newest link</span>
          </TextAction>
          <InfoTip label="About the newest link" align="start">
            Note this before you download your record for checking. permaily-verify checks the file
            against it.
          </InfoTip>
          <span role="status" className="text-small text-ink-2">
            {copied}
          </span>
        </div>
      ) : (
        <p className="text-small">{linkVerdictWords(head.verdict, head.n)}</p>
      )}
    </div>
  )
}

/** The connection and the kind of rows shown. Only the connection is in the address. */
function Filters({
  filter,
  known,
  rows,
  onConnection,
  onKind,
}: {
  filter: Filter
  known: Known
  rows: Row[]
  onConnection: (id: string | null) => void
  onKind: (kind: KindFilter) => void
}) {
  const connectionId = useId()
  const kindId = useId()
  const listed = [...known.connections.values()]
  const unlisted = filter.connection !== null && !known.connections.has(filter.connection)
  // A connection no longer listed is named by its vendor's domain, from its receipts.
  const unlistedVendor = unlisted
    ? rows.find((r) => r.type === 'receipt' && r.receipt.connection === filter.connection)
    : undefined
  const select = cx(
    'max-w-72 rounded-sm border border-ink bg-paper px-2 py-1.5 text-body text-ink',
    focusRing,
  )
  return (
    <div className="mt-5 flex flex-wrap items-center gap-x-5 gap-y-2 border-b border-rule-strong pb-3.5">
      <div className="flex items-center gap-2">
        <label htmlFor={connectionId} className="text-small text-ink-2">
          Connection
        </label>
        <select
          id={connectionId}
          value={filter.connection ?? ''}
          onChange={(event) => onConnection(event.target.value || null)}
          className={select}
        >
          <option value="">Every connection</option>
          {listed.map((c) => (
            <option key={c.id} value={c.id}>
              {c.client_name || c.vendor.domain} ({c.vendor.domain})
              {c.state === 'ended' ? ', ended' : ''}
            </option>
          ))}
          {unlisted && filter.connection && (
            <option value={filter.connection}>
              {unlistedVendor?.type === 'receipt'
                ? `${unlistedVendor.receipt.vendor}, no longer listed`
                : 'A connection no longer listed'}
            </option>
          )}
        </select>
      </div>
      <div className="flex items-center gap-2">
        <label htmlFor={kindId} className="text-small text-ink-2">
          Show
        </label>
        <select
          id={kindId}
          value={filter.kind}
          onChange={(event) => onKind(event.target.value as KindFilter)}
          className={select}
        >
          {kindFilters.map((k) => (
            <option key={k.value} value={k.value}>
              {k.label}
            </option>
          ))}
        </select>
      </div>
    </div>
  )
}

/** Rows grouped under their day, in the walk's order. A row with no time of its own stays with the row before it. */
export function groupByDay(rows: Row[]): { day: string; rows: Row[] }[] {
  const groups: { day: string; rows: Row[] }[] = []
  for (const row of rows) {
    const day = dayWords(rowTime(row))
    const last = groups[groups.length - 1]
    if (last && (day === null || day === last.day)) last.rows.push(row)
    else groups.push({ day: day ?? 'Time unknown', rows: [row] })
  }
  return groups
}

/** The vendor as a row shows it: the owner the image's list verified, or the domain in monospace. */
function VendorCell({ receipt, known }: { receipt: api.Receipt; known: Known }) {
  const listed =
    (receipt.connection && known.connections.get(receipt.connection)) ||
    [...known.connections.values()].find((c) => c.vendor.domain === receipt.vendor)
  const owner =
    listed && listed.vendor.domain === receipt.vendor ? listed.vendor.verified : undefined
  return owner ? (
    <span className="truncate">{owner}</span>
  ) : (
    <bdi className="truncate font-mono text-mono">{receipt.vendor}</bdi>
  )
}

type View = { title: ReactNode; detail?: ReactNode; chip?: string; parts?: string }

/** What a row says: its title, a line under it, its outcome and the parts given. */
function viewOf(row: Row, known: Known): View {
  switch (row.type) {
    case 'link':
      return {
        title: linkVerdictWords(row.link.verdict, row.link.n),
        chip: linkVerdictChip(row.link.verdict),
      }
    case 'break':
      return { title: breakWords(row.link.n), chip: 'Break' }
    case 'missing':
      return {
        title: receiptVerdictWords(row.entry.verdict, row.link.n, row.entry.index),
        chip: receiptVerdictChip(row.entry.verdict),
      }
    case 'unknown':
      return {
        title: "This app can't show this receipt",
        detail: <span className="font-mono text-mono">{row.entry.id}</span>,
      }
    case 'receipt':
      return receiptView(row, known)
  }
}

function receiptView(row: Extract<Row, { type: 'receipt' }>, known: Known): View {
  const r = row.receipt
  if (typeof row.item === 'object' && 'delivered' in row.item) {
    const d = r.delivered[row.item.delivered]
    return {
      title: d && d.from.length > 0 ? <bdi>{senderWords(d.from)}</bdi> : 'A message',
      detail: d?.subject !== undefined ? <bdi>{d.subject}</bdi> : undefined,
      chip: 'Made available',
      parts: d ? givenWords(d.parts, d.attachment) : undefined,
    }
  }
  if (typeof row.item === 'object' && 'held' in row.item) {
    const h = r.held[row.item.held]
    const mailbox = h && known.mailboxes?.get(h.mailbox)
    return {
      title: 'A message held',
      detail: mailbox ? <bdi>{mailbox.label || mailbox.address}</bdi> : undefined,
      chip: 'Held by a rule',
    }
  }
  if (row.item === 'nothing')
    return {
      title: r.kind === 'search' ? 'Searched, nothing matched' : 'Asked, nothing made available',
    }
  switch (r.kind) {
    case 'refused': {
      const reason = r.refused?.reason ?? ''
      return {
        title: refusedWords(reason) ?? <span className="font-mono text-mono">{reason}</span>,
        chip: 'Refused',
      }
    }
    case 'consent':
      return r.consent
        ? {
            title: consentWords(r.consent),
            chip: r.consent.allowed ? 'Approved' : 'Refused',
          }
        : { title: "This app can't show this receipt" }
    case 'ended':
      return {
        title: endedDevice(r) ? 'A device ended' : 'A connection ended',
        detail: endWords(r.ended?.reason),
        chip: 'Ended',
      }
    case 'refusals': {
      const refused = (r.refusals?.counts ?? []).reduce((n, c) => n + c.count, 0)
      return {
        title: `Refused ${count(refused)} ${refused === 1 ? 'request' : 'requests'} in a minute`,
        chip: 'Refused',
      }
    }
    default:
      return { title: "This app can't show this receipt" }
  }
}

/** The panel's heading: what the receipt is, as text. */
function panelTitle(row: Row): string {
  if (row.type !== 'receipt') {
    if (row.type === 'link') return linkVerdictChip(row.link.verdict)
    if (row.type === 'break') return 'A break in the record'
    if (row.type === 'missing') return receiptVerdictChip(row.entry.verdict)
    return "This app can't show this receipt"
  }
  const r = row.receipt
  switch (r.kind) {
    case 'search':
      return 'A search'
    case 'read':
      return 'A message read'
    case 'attachment':
      return 'An attachment read'
    case 'refused':
      return 'A request refused'
    case 'consent':
      return r.consent ? consentWords(r.consent) : 'A decision'
    case 'ended':
      return endedDevice(r) ? 'A device ended' : 'A connection ended'
    case 'refusals':
      return 'Requests refused in a minute'
  }
}

function RowItem({
  row,
  known,
  selected,
  inline,
  onSelect,
}: {
  row: Row
  known: Known
  selected: boolean
  inline: boolean
  onSelect: () => void
}) {
  const view = viewOf(row, known)
  const panelId = `row-${row.key.replace(/[^a-z0-9]/gi, '-')}`
  const late = row.type === 'link' || row.type === 'break' ? null : linkKindChip(row.link.kind)
  return (
    <li className="border-b border-rule">
      <button
        type="button"
        data-row={row.key}
        // Picked from xl. Below it the row opens and closes its receipt.
        aria-pressed={inline ? undefined : selected}
        aria-expanded={inline ? selected : undefined}
        aria-controls={inline && selected ? panelId : undefined}
        onClick={onSelect}
        className={cx(
          '-mx-2.5 grid w-[calc(100%+20px)] scroll-mt-16 grid-cols-[auto_1fr_auto] items-center gap-x-3 gap-y-0.5 border-l-2 py-2 pr-2.5 pl-2 text-left md:scroll-mt-4',
          selected ? 'border-ink bg-sunken' : 'border-transparent hover:bg-sunken',
          focusRing,
        )}
      >
        <span className="row-start-1 font-mono text-mono text-ink-2">
          {clockWords(rowTime(row))}
        </span>
        <span className="row-start-1 min-w-0 truncate font-medium">{view.title}</span>
        <span className="row-start-1 flex flex-wrap justify-end gap-1.5">
          {late && <Tag>{late}</Tag>}
          {view.chip && <Tag>{view.chip}</Tag>}
        </span>
        <span className="col-start-2 col-end-4 row-start-2 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-small text-ink-2">
          {row.type === 'receipt' && <VendorCell receipt={row.receipt} known={known} />}
          {view.detail !== undefined && <span className="min-w-0 truncate">{view.detail}</span>}
          {view.parts && <PartsChip>{view.parts}</PartsChip>}
        </span>
      </button>
      {/* Below xl the receipt opens here, under its row, carrying on the row's rule at the left. */}
      {inline && selected && (
        <div
          id={panelId}
          role="region"
          aria-label="Receipt"
          className="-mx-2.5 border-l-2 border-ink bg-raised px-4 pt-5 pb-6 sm:px-6"
        >
          <ReceiptPanel row={row} title={panelTitle(row)} known={known} />
        </div>
      )}
    </li>
  )
}

/** Why the record could not be read: fixed words, and Try again unless trying again cannot help. */
function FailureLine({ failure, onRetry }: { failure: Failure; onRetry: () => void }) {
  const words =
    failure.code === 'busy'
      ? 'Your record is busy. Try again in a moment.'
      : failure.code === 'network'
        ? "Permaily can't be reached."
        : messageFor(failure.code)
  return (
    <div
      role="alert"
      className="mt-3 flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-rule py-3 text-small text-danger"
    >
      <p>{words}</p>
      {failure.code !== 'receipts_unreadable' && (
        <TextAction onClick={onRetry}>Try again</TextAction>
      )}
    </div>
  )
}

/** What the last press found, when it found nothing of the filter, and the record's start once it is reached. */
export function moreLine(
  filter: Filter,
  shown: number,
  examined: number,
  press: { found: number; links: number } | null,
): string | null {
  const newest = count(examined)
  if (filtering(filter) && (shown === 0 || press?.found === 0)) {
    if (filter.kind === 'all')
      return shown === 0
        ? `No receipt of this connection in the newest ${newest}.`
        : `No more receipts of this connection in the newest ${newest}.`
    const what = filter.connection === null ? 'of this kind' : 'of this kind from this connection'
    return `${shown === 0 ? 'Nothing' : 'Nothing more'} ${what} in the newest ${newest} receipts.`
  }
  if (press && press.found === 0)
    return `Looked through ${count(press.links)} ${press.links === 1 ? 'link' : 'links'}.`
  return null
}

function More({
  done,
  loading,
  line,
  onMore,
  moreRef,
  endRef,
}: {
  done: boolean
  loading: boolean
  line: string | null
  onMore: () => void
  moreRef: RefObject<HTMLButtonElement | null>
  endRef: RefObject<HTMLParagraphElement | null>
}) {
  if (done)
    return (
      <p ref={endRef} tabIndex={-1} className="mt-4 text-small text-ink-2 outline-none">
        That's the start of your record.
      </p>
    )
  return (
    <div className="mt-4 flex flex-col items-start gap-2">
      {line && <p className="text-small text-ink-2">{line}</p>}
      <Button ref={moreRef} variant="secondary" size="sm" aria-disabled={loading} onClick={onMore}>
        {line ? 'Look further' : 'Show older'}
      </Button>
    </div>
  )
}
