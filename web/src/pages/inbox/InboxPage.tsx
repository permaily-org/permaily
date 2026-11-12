import { Paperclip } from 'lucide-react'
import { type FormEvent, useCallback, useEffect, useId, useState } from 'react'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import {
  Button,
  Checkbox,
  SearchField,
  Tag,
  TextAction,
  TextLink,
} from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'
import { actionClass, cx, focusRing, pagePadding } from '../../lib/styles.ts'
import { LoadState, mailboxName, Support, stateWords } from '../accounts/shared.tsx'
import { canDeleteMail, canRead, useStanding } from '../accounts/standing.ts'
import { messageFor } from '../auth/messages.ts'
import { day, listTime, senderOf } from './format.ts'

/** The longest search the server takes. */
const maxQuery = 200

/** What the list shows: the mailbox's messages, or what a search found, and where the next page starts. */
type View = {
  /** The search, in this page's memory only: never in the address bar or storage, since it may name people. */
  q: string
  messages: api.MessageSummary[]
  next?: string
  searchedTo?: string
}

/** What the message page tells this one when it comes back after deleting. */
export const deletedOne = 'Message deleted.'

/**
 * One mailbox's inbox: its messages, the newest first, a page at a time, searching, ticking and deleting. A page
 * reads a bounded number of stored items, so it may come back short, or empty, with more to read: "Older" is
 * offered whenever the server says there is more.
 */
export function InboxPage() {
  const { mailboxId = '' } = useParams()
  // A new mailbox is a new page: nothing of the last one's list, search or ticks stays.
  return <Inbox key={mailboxId} mailboxId={mailboxId} />
}

function Inbox({ mailboxId }: { mailboxId: string }) {
  const { failed } = useSession()
  const standing = useStanding()
  const location = useLocation()
  const navigate = useNavigate()
  const reading = canRead(standing)
  const deleting = canDeleteMail(standing)
  const [mailboxes, setMailboxes] = useState<api.Mailbox[] | null>(null)
  const [view, setView] = useState<View | null>(null)
  const [failure, setFailure] = useState<{ code: api.ApiErrorCode; text: string } | null>(null)
  const [typed, setTyped] = useState('')
  const [searchError, setSearchError] = useState<string | null>(null)
  const [searching, setSearching] = useState(false)
  const [more, setMore] = useState<'idle' | 'loading' | string>('idle')
  const [ticked, setTicked] = useState<ReadonlySet<string>>(new Set())
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const [said, setSaid] = useState(() =>
    (location.state as { said?: unknown } | null)?.said === deletedOne ? deletedOne : '',
  )
  const now = Date.now()

  const loadMailboxes = useCallback(async () => {
    try {
      setMailboxes((await api.mailboxes()).mailboxes)
    } catch (error) {
      // The list still shows: the picker and the mailbox's state wait for the next try.
      failed(error)
    }
  }, [failed])

  const loadList = useCallback(async () => {
    setFailure(null)
    try {
      const page = await api.messages(mailboxId)
      setView({ q: '', ...page, searchedTo: page.searched_to })
    } catch (error) {
      const code = failed(error)
      if (code) setFailure({ code, text: messageFor(code) })
    }
  }, [failed, mailboxId])

  useEffect(() => {
    void loadMailboxes()
    void loadList()
  }, [loadMailboxes, loadList])

  // The note the message page left is said once: a reload does not say it again.
  useEffect(() => {
    if (location.state) navigate(location.pathname, { replace: true, state: null })
  }, [location.state, location.pathname, navigate])

  const mailbox = mailboxes?.find((m) => m.id === mailboxId)
  const readable = mailboxes?.filter((m) => m.state !== 'unreadable') ?? []

  async function search(event: FormEvent) {
    event.preventDefault()
    if (searching) return
    const q = typed.trim()
    if (q === '') {
      if (view?.q) {
        setTyped('')
        void loadList()
      }
      return
    }
    if ([...q].length > maxQuery) {
      setSearchError(messageFor('invalid_query'))
      document.getElementById('inbox-search')?.focus()
      return
    }
    setSearchError(null)
    setSearching(true)
    try {
      const page = await api.messages(mailboxId, { q })
      setView({ q, ...page, searchedTo: page.searched_to })
      setTicked(new Set())
      setMore('idle')
    } catch (error) {
      const code = failed(error)
      if (code) setSearchError(`The search didn't run. ${messageFor(code)}`)
    } finally {
      setSearching(false)
    }
  }

  function clearSearch() {
    setTyped('')
    setSearchError(null)
    setTicked(new Set())
    setView(null)
    void loadList()
    document.getElementById('inbox-search')?.focus()
  }

  async function older() {
    if (!view?.next || more === 'loading') return
    setMore('loading')
    try {
      const page = await api.messages(mailboxId, { after: view.next, q: view.q || undefined })
      setView((now) =>
        now
          ? {
              q: now.q,
              messages: [...now.messages, ...page.messages],
              next: page.next,
              searchedTo: page.searched_to ?? now.searchedTo,
            }
          : now,
      )
      setMore('idle')
    } catch (error) {
      const code = failed(error)
      setMore(code ? messageFor(code) : 'idle')
    }
  }

  async function deleteTicked() {
    if (pending || ticked.size === 0) return
    if (ticked.size > api.maxDeleted) {
      setDeleteError(messageFor('too_many_messages'))
      return
    }
    setPending(true)
    setDeleteError(null)
    const ids = [...ticked]
    try {
      const deleted = await api.deleteMessages(mailboxId, ids)
      setView((now) =>
        now ? { ...now, messages: now.messages.filter((m) => !ticked.has(m.id)) } : now,
      )
      setTicked(new Set())
      setSaid(deleted === 1 ? '1 message deleted.' : `${deleted} messages deleted.`)
    } catch (error) {
      const code = failed(error)
      if (code) setDeleteError(`Nothing was deleted. ${messageFor(code)}`)
    } finally {
      setPending(false)
    }
  }

  function tick(id: string, on: boolean) {
    setTicked((now) => {
      const next = new Set(now)
      if (on) next.add(id)
      else next.delete(id)
      return next
    })
  }

  const gone = failure?.code === 'not_found'
  const unreadable = failure?.code === 'mailbox_unreadable' || mailbox?.state === 'unreadable'
  const shown = view?.messages ?? []
  const allTicked = shown.length > 0 && shown.every((m) => ticked.has(m.id))

  return (
    <div className={pagePadding}>
      <div className="flex flex-wrap items-end justify-between gap-x-6 gap-y-3">
        <div className="min-w-0">
          <h1 className="text-display-s font-medium md:text-display">Inbox</h1>
          {mailbox && mailbox.state !== 'unreadable' && (
            <p className="mt-1.5 flex flex-wrap items-baseline gap-x-3 gap-y-0.5 text-small text-ink-2">
              {mailbox.label && <span className="font-medium text-ink">{mailbox.label}</span>}
              <span className="font-mono wrap-anywhere">{mailbox.address}</span>
              <span
                className={cx(
                  mailbox.state === 'receiving' && 'text-ok',
                  mailbox.state !== 'receiving' && 'text-attention',
                )}
              >
                {stateWords(mailbox)}
              </span>
            </p>
          )}
        </div>
        {readable.length > 1 && (
          <Picker
            mailboxes={readable}
            current={mailboxId}
            onPick={(id) => navigate(`/inbox/${encodeURIComponent(id)}`)}
          />
        )}
      </div>

      {mailbox?.state === 'paused' && (
        <p className="mt-6 max-w-3xl rounded-sm bg-attention-bg px-3 py-2.5 text-body text-attention">
          Paused by Permaily: it accepts no new mail until Permaily resumes it. What it holds stays.
          Write to <Support /> to ask why.
        </p>
      )}

      {gone ? (
        <p className="mt-8 border-t border-rule-strong py-5 text-body text-ink-2">
          That mailbox isn't there any more. <TextLink to="/inbox">Go to your inbox</TextLink>
        </p>
      ) : unreadable ? (
        <p className="mt-8 border-t border-rule-strong py-5 text-body text-ink-2">
          This mailbox can't be read: its mail can't be opened. Disconnect it in{' '}
          <TextLink to="/accounts">Accounts</TextLink>, and write to <Support />.
        </p>
      ) : (
        <>
          {reading && (
            <form noValidate onSubmit={search} className="mt-7 flex max-w-2xl flex-col gap-1.5">
              <div className="flex flex-wrap items-center gap-2">
                <SearchField
                  label="Search this mailbox"
                  placeholder="Senders, subjects, text"
                  id="inbox-search"
                  value={typed}
                  maxLength={maxQuery * 2}
                  aria-invalid={searchError ? true : undefined}
                  aria-describedby="inbox-search-error"
                  onChange={(event) => setTyped(event.target.value)}
                  className="min-w-0 flex-1 basis-60"
                />
                <Button type="submit" variant="secondary" size="sm" aria-disabled={searching}>
                  Search
                </Button>
                {view?.q && (
                  <TextAction size="sm" onClick={clearSearch}>
                    Clear search
                  </TextAction>
                )}
              </div>
              <p
                id="inbox-search-error"
                role="alert"
                className="text-small text-danger empty:hidden"
              >
                {searchError}
              </p>
            </form>
          )}

          <p role="status" className="mt-4 text-body font-medium empty:hidden">
            {said}
          </p>

          {deleting && shown.length > 0 && (
            <div className="mt-4 flex flex-wrap items-center gap-x-5 gap-y-2 py-2">
              <Checkbox
                label={<span className="text-small text-ink-2">Select all shown</span>}
                checked={allTicked}
                onChange={(event) =>
                  setTicked(event.target.checked ? new Set(shown.map((m) => m.id)) : new Set())
                }
              />
              <Button
                variant="secondary"
                size="sm"
                aria-disabled={pending || ticked.size === 0}
                onClick={deleteTicked}
                className="aria-disabled:border-rule aria-disabled:text-ink-3 aria-disabled:hover:bg-transparent"
              >
                Delete{ticked.size > 0 ? ` ${ticked.size}` : ''}
                <span className="sr-only"> selected</span>
              </Button>
              <p role="alert" className="text-small text-danger empty:hidden">
                {deleteError}
              </p>
            </div>
          )}

          <div
            className={cx('border-t border-rule-strong', !(deleting && shown.length > 0) && 'mt-4')}
          >
            {view === null ? (
              <LoadState failure={failure?.text ?? null} what="The mail" onRetry={loadList} />
            ) : (
              <>
                {view.q && <SearchSummary view={view} />}
                {shown.length > 0 ? (
                  <ul aria-label={view.q ? 'Found' : 'Messages'}>
                    {shown.map((m) => (
                      <Row
                        key={m.id}
                        mailboxId={mailboxId}
                        message={m}
                        now={now}
                        reading={reading}
                        ticked={deleting ? ticked.has(m.id) : null}
                        onTick={(on) => tick(m.id, on)}
                      />
                    ))}
                  </ul>
                ) : view.q ? null : mailbox?.state === 'waiting' ? (
                  <div className="border-b border-rule py-5">
                    <p className="text-body">Waiting for Gmail.</p>
                    <p className="mt-1 text-body text-ink-2">
                      Mail shows here once forwarding is on.{' '}
                      <TextLink to={`/accounts/${encodeURIComponent(mailboxId)}/forwarding`}>
                        Finish the forwarding set-up
                      </TextLink>
                    </p>
                  </div>
                ) : (
                  <p className="border-b border-rule py-5 text-body text-ink-2">No mail here.</p>
                )}
                {view.next ? (
                  <div className="mt-4 flex flex-wrap items-baseline gap-x-4 gap-y-1">
                    <Button
                      variant="secondary"
                      size="sm"
                      aria-disabled={more === 'loading'}
                      onClick={older}
                    >
                      {view.q ? 'Search older mail' : 'Older'}
                    </Button>
                    <p role="alert" className="text-small text-danger empty:hidden">
                      {more === 'idle' || more === 'loading' ? '' : more}
                    </p>
                  </div>
                ) : (
                  !view.q && mailbox?.receiving_since && <Horizon since={mailbox.receiving_since} />
                )}
              </>
            )}
          </div>
        </>
      )}
    </div>
  )
}

/** Choosing which mailbox's inbox shows, when the account has several. */
function Picker({
  mailboxes,
  current,
  onPick,
}: {
  mailboxes: api.Mailbox[]
  current: string
  onPick: (id: string) => void
}) {
  const id = useId()
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <label htmlFor={id} className="text-small text-ink-2">
        Mailbox
      </label>
      <select
        id={id}
        value={current}
        onChange={(event) => onPick(event.target.value)}
        className={cx(
          'max-w-72 rounded-sm border border-ink bg-paper px-2 py-1.5 text-body text-ink',
          focusRing,
        )}
      >
        {mailboxes.map((mailbox) => (
          <option key={mailbox.id} value={mailbox.id}>
            {mailboxName(mailbox)}
          </option>
        ))}
      </select>
    </div>
  )
}

/** How far back a search looked, and whether it found anything. */
function SearchSummary({ view }: { view: View }) {
  const found = view.messages.length
  return (
    <p role="status" className="border-b border-rule py-2.5 text-small text-ink-2">
      {found === 0 ? 'Nothing found' : found === 1 ? '1 found' : `${found} found`}
      {view.searchedTo ? (
        <>
          {'. Searched mail back to '}
          <time dateTime={view.searchedTo} className="font-mono text-ink">
            {day(view.searchedTo)}
          </time>
        </>
      ) : null}
      .
    </p>
  )
}

/**
 * Where Permaily's sight of the mailbox begins: nothing from before forwarding started is here, and the list
 * ends on that line, with its day.
 */
function Horizon({ since }: { since: string }) {
  return (
    <div className="mt-10">
      <div aria-hidden className="flex items-center gap-3">
        <span className="h-px flex-1 bg-rule-strong" />
        <span className="font-mono text-mono text-ink-2">{day(since)}</span>
        <span className="h-px w-6 bg-rule-strong" />
      </div>
      <p className="mt-2 text-right text-small text-ink-2">
        Nothing from before{' '}
        <time dateTime={since} className="text-ink">
          {day(since)}
        </time>{' '}
        is here: Permaily only sees mail forwarded after that.
      </p>
    </div>
  )
}

/**
 * One message in the list. Everything in it came from whoever sent the mail: it is shown as text, isolated
 * from the text around it (`bdi`), and never as markup.
 */
function Row({
  mailboxId,
  message,
  now,
  reading,
  ticked,
  onTick,
}: {
  mailboxId: string
  message: api.MessageSummary
  now: number
  /** Whether it can be opened; otherwise it can be downloaded. */
  reading: boolean
  /** Whether it is ticked, or null where nothing can be deleted. */
  ticked: boolean | null
  onTick: (on: boolean) => void
}) {
  const subject = message.subject || 'No subject'
  const content = (
    <span className="grid min-w-0 grid-cols-[minmax(0,1fr)_auto] gap-x-3 @2xl:flex @2xl:items-baseline @2xl:gap-4">
      <span className="truncate font-medium @2xl:w-48 @2xl:shrink-0">
        <bdi>{senderOf(message.from)}</bdi>
      </span>
      <time
        dateTime={message.received_at}
        className="col-start-2 row-start-1 font-mono text-mono text-ink-2 @2xl:order-last @2xl:w-20 @2xl:shrink-0 @2xl:text-right"
      >
        {listTime(message.received_at, now)}
      </time>
      <span className="col-span-2 flex min-w-0 items-baseline gap-2 @2xl:flex-1">
        <span className="min-w-0 truncate">
          <bdi className={cx(!message.subject && 'text-ink-2')}>{subject}</bdi>
          {message.snippet && (
            <span className="ml-2 hidden text-ink-2 @2xl:inline">
              <bdi>{message.snippet}</bdi>
            </span>
          )}
        </span>
        {message.confirmation && <Tag className="shrink-0 py-0">Gmail code</Tag>}
        {message.attachments > 0 && (
          <span className="inline-flex shrink-0 items-center gap-0.5 text-ink-2">
            <Paperclip aria-hidden size={13} />
            <span className="font-mono text-mono">{message.attachments}</span>
            <span className="sr-only">
              {message.attachments === 1 ? ' attachment' : ' attachments'}
            </span>
          </span>
        )}
      </span>
      {message.snippet && (
        <span className="col-span-2 truncate text-small text-ink-2 @2xl:hidden">
          <bdi>{message.snippet}</bdi>
        </span>
      )}
    </span>
  )
  return (
    <li className="@container border-b border-rule">
      <div className="flex items-start gap-3 @2xl:items-center">
        {ticked !== null && (
          <span className="pt-3 @2xl:pt-0">
            <Checkbox
              label={<span className="sr-only">Select {subject}</span>}
              checked={ticked}
              onChange={(event) => onTick(event.target.checked)}
            />
          </span>
        )}
        {reading ? (
          <Link
            to={`/inbox/${encodeURIComponent(mailboxId)}/${encodeURIComponent(message.id)}`}
            className={cx('block min-w-0 flex-1 py-2.5 hover:bg-sunken @2xl:py-3', focusRing)}
          >
            {content}
          </Link>
        ) : (
          <>
            <span className="block min-w-0 flex-1 py-2.5 @2xl:py-3">{content}</span>
            <a
              href={api.rawMessageUrl(mailboxId, message.id)}
              download
              className={actionClass('ink', 'sm', 'mt-3 shrink-0 @2xl:mt-0')}
            >
              Download<span className="sr-only"> {subject}</span>
            </a>
          </>
        )}
      </div>
    </li>
  )
}
