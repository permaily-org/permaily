import {
  type FormEvent,
  type ReactNode,
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
} from 'react'
import { useConfirmed } from '../../components/Confirm.tsx'
import { Button, ButtonLink, InfoTip, TextAction, TextLink } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { isCancelled } from '../../lib/cancelled.ts'
import { useSession } from '../../lib/session.tsx'
import { cx, focusRing, pagePadding } from '../../lib/styles.ts'
import { messageFor } from '../auth/messages.ts'
import { formatDay } from '../settings/signin/shared.ts'
import { DisconnectDialog } from './DisconnectDialog.tsx'
import { CopyButton, forkedWords, LoadState, mailboxName, Support, stateWords } from './shared.tsx'
import { canChange, useStanding } from './standing.ts'

/** The most characters a label has (`domain.MailboxLabel`). */
export const maxLabel = 64

function headline(count: number): string {
  if (count === 0) return 'No mailboxes connected.'
  return count === 1 ? '1 mailbox connected.' : `${count} mailboxes connected.`
}

/**
 * Why disconnecting asks the person to show it's them again: not because it decides who gets into the account,
 * but because it stops the address and deletes its mail for good (ADR 0026, "Confirming again").
 */
const disconnectProof = {
  says: "Disconnecting stops this mailbox's address and deletes its mail for good, which can't be undone, so Permaily asks you to show it's you again. It counts for the next 10 minutes.",
}

/**
 * The account's mailboxes, from the server: each one's address to forward to, its label, its state, whether
 * Google confirmed the Gmail account behind it, how long it keeps mail, and disconnecting it.
 */
export function AccountsPage() {
  const { failed } = useSession()
  const standing = useStanding()
  const confirmed = useConfirmed()
  const [list, setList] = useState<api.MailboxList | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [disconnecting, setDisconnecting] = useState<api.Mailbox | null>(null)
  const [pending, setPending] = useState(false)
  const [dialogError, setDialogError] = useState<string | null>(null)
  const [forked, setForked] = useState<ReadonlySet<string>>(new Set())
  const [said, setSaid] = useState('')
  const headingRef = useRef<HTMLHeadingElement>(null)

  const load = useCallback(async () => {
    setFailure(null)
    try {
      setList(await api.mailboxes())
    } catch (error) {
      const code = failed(error)
      if (code) setFailure(messageFor(code))
    }
  }, [failed])

  useEffect(() => {
    void load()
  }, [load])

  function replaced(mailbox: api.Mailbox) {
    setList((now) =>
      now
        ? { ...now, mailboxes: now.mailboxes.map((m) => (m.id === mailbox.id ? mailbox : m)) }
        : now,
    )
  }

  async function disconnect(mailbox: api.Mailbox) {
    if (pending) return
    setPending(true)
    setDialogError(null)
    try {
      await confirmed(() => api.disconnectMailbox(mailbox.id), disconnectProof)
      setList((now) =>
        now ? { ...now, mailboxes: now.mailboxes.filter((m) => m.id !== mailbox.id) } : now,
      )
      setDisconnecting(null)
      setSaid('Mailbox disconnected. Its mail is being deleted.')
      // The row and its button are gone: the page's heading takes focus, and the words under it say why.
      headingRef.current?.focus()
    } catch (error) {
      if (isCancelled(error)) return
      const code = failed(error)
      if (code === 'mailbox_unreadable') {
        // Its record was forked: it cannot be disconnected, and stays, counted. The row says so.
        setForked((now) => new Set(now).add(mailbox.id))
        setDisconnecting(null)
      } else if (code) {
        setDialogError(`The mailbox wasn't disconnected. ${messageFor(code)}`)
      }
    } finally {
      setPending(false)
    }
  }

  const count = list?.mailboxes.length ?? 0
  const room = list !== null && count < list.limit
  return (
    <div className={pagePadding}>
      <div className="flex flex-wrap items-start justify-between gap-x-6 gap-y-4">
        <div className="min-w-0">
          <h1
            ref={headingRef}
            tabIndex={-1}
            className="text-display-s font-medium outline-none md:text-display"
          >
            {list ? headline(count) : 'Mailboxes'}
          </h1>
          {list && (
            <p className="mt-2 text-body text-ink-2">
              {count >= list.limit
                ? `That's as many as an account can have. Disconnect one to connect another.`
                : `An account can have ${list.limit}.`}
            </p>
          )}
        </div>
        {canChange(standing) && room && (
          <ButtonLink to="/accounts/new" className="sm:mt-2">
            Connect a mailbox
          </ButtonLink>
        )}
      </div>
      <p role="status" className="mt-4 text-body font-medium empty:hidden">
        {said}
      </p>

      <div className="mt-6 border-t border-rule-strong">
        {list === null ? (
          <LoadState failure={failure} what="Your mailboxes" onRetry={() => void load()} />
        ) : count === 0 ? (
          <p className="border-b border-rule py-4 text-body text-ink-2">
            Forward your Gmail to a mailbox of your own, then read it here.
          </p>
        ) : (
          <ul aria-label="Your mailboxes">
            {list.mailboxes.map((mailbox) =>
              mailbox.state === 'unreadable' ? (
                <UnreadableRow
                  key={mailbox.id}
                  mailbox={mailbox}
                  forked={forked.has(mailbox.id)}
                  onDisconnect={() => {
                    setDialogError(null)
                    setDisconnecting(mailbox)
                  }}
                />
              ) : (
                <MailboxRow
                  key={mailbox.id}
                  mailbox={mailbox}
                  changing={canChange(standing)}
                  onChanged={replaced}
                  onDisconnect={() => {
                    setDialogError(null)
                    setDisconnecting(mailbox)
                  }}
                />
              ),
            )}
          </ul>
        )}
      </div>

      <DisconnectDialog
        mailbox={disconnecting}
        pending={pending}
        error={dialogError}
        onCancel={() => setDisconnecting(null)}
        onConfirm={disconnect}
      />
    </div>
  )
}

function MailboxRow({
  mailbox,
  changing,
  onChanged,
  onDisconnect,
}: {
  mailbox: api.Mailbox
  /** Whether the account may rename it and change its retention. */
  changing: boolean
  onChanged: (mailbox: api.Mailbox) => void
  onDisconnect: () => void
}) {
  const id = useId()
  const name = mailboxName(mailbox)
  return (
    <li aria-labelledby={`${id}-name`} className="border-b border-rule py-5">
      <div className="grid gap-x-10 gap-y-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,22rem)]">
        <div className="min-w-0">
          <Label
            mailbox={mailbox}
            changing={changing}
            nameId={`${id}-name`}
            onChanged={onChanged}
          />
          <p className="mt-1.5 flex flex-wrap items-baseline gap-x-3 gap-y-1">
            <span className="font-mono text-small wrap-anywhere">{mailbox.address}</span>
            <CopyButton value={mailbox.address} what={`the address of ${name}`} />
          </p>
          <div className="mt-3 flex flex-wrap gap-x-5 gap-y-2">
            {mailbox.state !== 'waiting' && (
              <TextLink to={`/inbox/${encodeURIComponent(mailbox.id)}`} size="sm">
                Open inbox<span className="sr-only"> of {name}</span>
              </TextLink>
            )}
            <TextLink to={`/accounts/${encodeURIComponent(mailbox.id)}/forwarding`} size="sm">
              Forwarding set-up<span className="sr-only"> of {name}</span>
            </TextLink>
            <TextAction tone="danger" size="sm" onClick={onDisconnect}>
              Disconnect<span className="sr-only"> {name}</span>
            </TextAction>
          </div>
        </div>
        <dl className="min-w-0 text-small">
          <Fact label="State">
            <span
              className={cx(
                mailbox.state === 'receiving' && 'text-ok',
                (mailbox.state === 'waiting' || mailbox.state === 'paused') && 'text-attention',
              )}
            >
              {stateWords(mailbox)}
            </span>
          </Fact>
          <Fact label="Gmail account">
            <Verified mailbox={mailbox} />
          </Fact>
          <Fact label="Keeps mail">
            {changing ? (
              <Retention mailbox={mailbox} onChanged={onChanged} />
            ) : (
              `${mailbox.retention_days} days`
            )}
          </Fact>
        </dl>
      </div>
    </li>
  )
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex items-baseline gap-4 border-b border-rule py-2 first:pt-0 last:border-b-0">
      <dt className="w-28 shrink-0 text-ink-2">{label}</dt>
      <dd className="min-w-0 flex-1">{children}</dd>
    </div>
  )
}

/** "Google confirmed: <Gmail account>", or that Google did not. */
function Verified({ mailbox }: { mailbox: api.Mailbox }) {
  return (
    <span className="inline-flex flex-wrap items-center gap-x-1.5">
      {mailbox.source ? (
        <span>
          Google confirmed:{' '}
          <span className="font-mono wrap-anywhere">{mailbox.source.address}</span>
        </span>
      ) : (
        <span>Not confirmed by Google</span>
      )}
      <InfoTip label="What Google confirms" align="end">
        Google confirmed that this Gmail account asked to forward its mail here. It doesn't show
        that forwarding is on, or where later mail comes from.
      </InfoTip>
    </span>
  )
}

/** The label, renamed in place. An empty one takes the label away. */
function Label({
  mailbox,
  changing,
  nameId,
  onChanged,
}: {
  mailbox: api.Mailbox
  changing: boolean
  nameId: string
  onChanged: (mailbox: api.Mailbox) => void
}) {
  const { failed } = useSession()
  const [editing, setEditing] = useState(false)
  const [typed, setTyped] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const fieldRef = useRef<HTMLInputElement>(null)
  const renameRef = useRef<HTMLButtonElement>(null)
  const fieldId = `${nameId}-field`

  useEffect(() => {
    if (editing) fieldRef.current?.focus()
  }, [editing])

  function close() {
    setEditing(false)
    setError(null)
    // Focus goes back to what opened the field.
    requestAnimationFrame(() => renameRef.current?.focus())
  }

  async function save(event: FormEvent) {
    event.preventDefault()
    if (pending) return
    const label = typed.trim()
    if ([...label].length > maxLabel || /[\p{Cc}\p{Zl}\p{Zp}]/u.test(label)) {
      setError(messageFor('invalid_label'))
      fieldRef.current?.focus()
      return
    }
    setPending(true)
    setError(null)
    try {
      onChanged(await api.updateMailbox(mailbox.id, { label }))
      close()
    } catch (failure) {
      const code = failed(failure)
      if (code) setError(`The label wasn't changed. ${messageFor(code)}`)
    } finally {
      setPending(false)
    }
  }

  if (editing) {
    return (
      <form noValidate onSubmit={save} className="flex flex-col gap-2">
        <h2 id={nameId} className="sr-only">
          {mailboxName(mailbox)}
        </h2>
        <label htmlFor={fieldId} className="text-small text-ink-2">
          Label
        </label>
        <input
          id={fieldId}
          ref={fieldRef}
          value={typed}
          maxLength={maxLabel * 2}
          autoComplete="off"
          aria-invalid={error ? true : undefined}
          aria-describedby={`${fieldId}-error`}
          onChange={(event) => setTyped(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === 'Escape') close()
          }}
          className={cx(
            'w-full max-w-sm rounded-sm border bg-transparent px-3 py-2 text-body',
            error ? 'border-danger' : 'border-ink',
            focusRing,
          )}
        />
        <p id={`${fieldId}-error`} role="alert" className="text-small text-danger empty:hidden">
          {error}
        </p>
        <div className="flex gap-3">
          <Button type="submit" size="sm" aria-disabled={pending}>
            Save
          </Button>
          <Button variant="secondary" size="sm" onClick={close}>
            Cancel
          </Button>
        </div>
      </form>
    )
  }
  return (
    <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
      <h2 id={nameId} className={cx('text-title font-medium', !mailbox.label && 'text-ink-2')}>
        {mailbox.label || 'No label'}
      </h2>
      {changing && (
        <TextAction
          ref={renameRef}
          size="sm"
          onClick={() => {
            setTyped(mailbox.label)
            setEditing(true)
          }}
        >
          Rename<span className="sr-only"> {mailboxName(mailbox)}</span>
        </TextAction>
      )}
    </div>
  )
}

/** "Keep mail for": the periods the server offers. A new one applies to mail received from now on. */
function Retention({
  mailbox,
  onChanged,
}: {
  mailbox: api.Mailbox
  onChanged: (mailbox: api.Mailbox) => void
}) {
  const { failed } = useSession()
  const id = useId()
  const [pending, setPending] = useState(false)
  const [said, setSaid] = useState<{ ok: boolean; text: string } | null>(null)

  async function change(days: number) {
    setPending(true)
    setSaid(null)
    try {
      onChanged(await api.updateMailbox(mailbox.id, { retention_days: days }))
      setSaid({ ok: true, text: 'Saved. It applies to mail from now on.' })
    } catch (failure) {
      const code = failed(failure)
      if (code) setSaid({ ok: false, text: `Nothing changed. ${messageFor(code)}` })
    } finally {
      setPending(false)
    }
  }

  const options = mailbox.retention_options.includes(mailbox.retention_days)
    ? mailbox.retention_options
    : [mailbox.retention_days, ...mailbox.retention_options]
  return (
    <div className="flex flex-col gap-1">
      <label htmlFor={id} className="sr-only">
        Keep mail for
      </label>
      <select
        id={id}
        value={mailbox.retention_days}
        aria-disabled={pending}
        onChange={(event) => !pending && void change(Number(event.target.value))}
        className={cx(
          'w-fit rounded-sm border border-ink bg-paper px-2 py-1 text-small text-ink',
          focusRing,
        )}
      >
        {options.map((days) => (
          <option key={days} value={days}>
            {days} days
          </option>
        ))}
      </select>
      <p
        role="status"
        className={cx('text-small empty:hidden', said?.ok === false ? 'text-danger' : 'text-ink-2')}
      >
        {said?.text}
      </p>
    </div>
  )
}

/** A mailbox whose record can't be read: its id, when it was added, and Disconnect alone. */
function UnreadableRow({
  mailbox,
  forked,
  onDisconnect,
}: {
  mailbox: api.Mailbox
  /** Disconnect answered that its record was forked: it stays, and the row says why. */
  forked: boolean
  onDisconnect: () => void
}) {
  const id = useId()
  return (
    <li aria-labelledby={id} className="border-b border-rule py-5">
      <h2 id={id} className="text-title font-medium text-danger">
        {stateWords(mailbox)}
      </h2>
      <p className="mt-1 text-small text-ink-2">
        <span className="font-mono">{mailbox.id}</span>, added {formatDay(mailbox.created_at)}
      </p>
      {forked ? (
        <p role="alert" className="mt-3 max-w-2xl text-body">
          {forkedWords}
        </p>
      ) : (
        <>
          <p className="mt-3 max-w-2xl text-body text-ink-2">
            It accepts no mail and its mail can't be opened. Disconnect it, and write to <Support />
            .
          </p>
          <TextAction tone="danger" size="sm" className="mt-3" onClick={onDisconnect}>
            Disconnect<span className="sr-only"> the mailbox that can't be read</span>
          </TextAction>
        </>
      )}
    </li>
  )
}
