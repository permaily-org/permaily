import { Paperclip } from 'lucide-react'
import { type ReactNode, useCallback, useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router'
import { Button, TextLink } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'
import { actionClass, buttonClass, cx, pagePadding } from '../../lib/styles.ts'
import { Confirmation } from '../accounts/Confirmations.tsx'
import { LoadState, mailboxName } from '../accounts/shared.tsx'
import { canDeleteMail, canRead, useStanding } from '../accounts/standing.ts'
import { messageFor } from '../auth/messages.ts'
import { fullTime, size } from './format.ts'
import { deletedOne } from './InboxPage.tsx'
import { MessageBody } from './MessageBody.tsx'

/**
 * One message: who sent it as the enclave read it, its subject, when it came, who signed it, its attachments
 * and the original to download, Delete, and its text. Everything of it came from whoever sent it, and is shown
 * as text only. Nothing of it goes into the page's title, the address bar or storage.
 */
export function MessagePage() {
  const { mailboxId = '', messageId = '' } = useParams()
  return (
    <MessageView key={`${mailboxId}/${messageId}`} mailboxId={mailboxId} messageId={messageId} />
  )
}

function MessageView({ mailboxId, messageId }: { mailboxId: string; messageId: string }) {
  const { failed } = useSession()
  const standing = useStanding()
  const navigate = useNavigate()
  const reading = canRead(standing)
  const [message, setMessage] = useState<api.Message | null>(null)
  const [mailbox, setMailbox] = useState<api.Mailbox | null>(null)
  const [failure, setFailure] = useState<{ code: api.ApiErrorCode; text: string } | null>(null)
  const [pending, setPending] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const inbox = `/inbox/${encodeURIComponent(mailboxId)}`

  const load = useCallback(async () => {
    setFailure(null)
    try {
      setMessage(await api.message(mailboxId, messageId))
    } catch (error) {
      const code = failed(error)
      if (code) setFailure({ code, text: messageFor(code) })
    }
  }, [failed, mailboxId, messageId])

  useEffect(() => {
    // The mailbox gives the back link its name and a confirmation its code; the message shows without it.
    api.mailbox(mailboxId).then(setMailbox, failed)
    // An account that has not agreed may download a message, not open it: it is not asked for.
    if (reading) void load()
  }, [mailboxId, reading, load, failed])

  async function remove() {
    if (pending) return
    setPending(true)
    setDeleteError(null)
    try {
      await api.deleteMessage(mailboxId, messageId)
      navigate(inbox, { state: { said: deletedOne } })
    } catch (error) {
      setPending(false)
      const code = failed(error)
      // Deleted already, here or elsewhere: it is gone either way.
      if (code === 'not_found') navigate(inbox, { state: { said: deletedOne } })
      else if (code) setDeleteError(`It wasn't deleted. ${messageFor(code)}`)
    }
  }

  const back = (
    <TextLink to={inbox} size="sm" className="self-start">
      Back to {mailbox ? mailboxName(mailbox) : 'the inbox'}
    </TextLink>
  )
  const original = (
    <a
      href={api.rawMessageUrl(mailboxId, messageId)}
      download
      className={buttonClass('secondary', 'sm')}
    >
      Download original
    </a>
  )

  const gone = failure?.code === 'not_found'
  const title =
    reading && message
      ? message.subject || 'No subject'
      : reading && gone
        ? "That message isn't there any more"
        : 'A message'
  // One heading for every state of the page, so the focus it takes when the page opens stays when the message
  // arrives.
  return (
    <div className={cx(pagePadding, 'flex min-h-full flex-col')}>
      {back}
      <header className="mt-6 max-w-5xl border-b border-rule pb-5">
        <h1 className="text-heading font-medium wrap-anywhere">
          <bdi className={cx(message && !message.subject && 'text-ink-2')}>{title}</bdi>
        </h1>
        {reading && message && (
          <MessageFacts
            message={message}
            actions={
              <>
                {original}
                {canDeleteMail(standing) && (
                  <Button variant="secondary" size="sm" aria-disabled={pending} onClick={remove}>
                    Delete
                  </Button>
                )}
                <p role="alert" className="text-small text-danger empty:hidden">
                  {deleteError}
                </p>
              </>
            }
          />
        )}
      </header>

      {!reading ? (
        <>
          <p className="mt-5 max-w-2xl text-body text-ink-2">
            Agree to the new terms to open messages. Until then you can download it.
          </p>
          <div className="mt-5">{original}</div>
        </>
      ) : !message ? (
        <div className="max-w-2xl">
          {gone ? (
            <p className="border-b border-rule py-3 text-body text-ink-2">
              It was deleted, or its time to be kept ended.
            </p>
          ) : (
            <LoadState failure={failure?.text ?? null} what="The message" onRetry={load} />
          )}
        </div>
      ) : (
        <MessageContent mailboxId={mailboxId} message={message} mailbox={mailbox} />
      )}
    </div>
  )
}

/** What follows the head: notes, a confirmation's code, the attachments, then the text. */
function MessageContent({
  mailboxId,
  message,
  mailbox,
}: {
  mailboxId: string
  message: api.Message
  mailbox: api.Mailbox | null
}) {
  const confirmation = message.confirmation
    ? mailbox?.confirmations.find((c) => c.message === message.id)
    : undefined
  return (
    <>
      {message.unparsed && (
        <p className="mt-5 max-w-3xl text-body text-ink-2">
          Permaily couldn't read this message as mail. Download the original to open it elsewhere.
        </p>
      )}
      {message.confirmation && (
        <section aria-labelledby="confirmation" className="mt-6 max-w-3xl">
          <h2 id="confirmation" className="text-title font-medium">
            Gmail's forwarding confirmation
          </h2>
          <div className="mt-2 border-t border-rule">
            {confirmation ? (
              <Confirmation confirmation={confirmation} />
            ) : (
              <p className="border-b border-rule py-3 text-body text-ink-2">
                Its code shows in the mailbox's{' '}
                <TextLink to={`/accounts/${encodeURIComponent(mailboxId)}/forwarding`}>
                  forwarding set-up
                </TextLink>{' '}
                for thirty days.
              </p>
            )}
          </div>
        </section>
      )}
      {message.attachments.length > 0 && <Attachments mailboxId={mailboxId} message={message} />}

      <section aria-label="Message text" className="mt-7 flex-1 border-t border-rule pt-6">
        <MessageBody message={message} />
        {message.text_truncated && (
          <p className="mt-6 max-w-prose border-t border-rule pt-3 text-small text-ink-2">
            The text is cut short here. The rest is in the original.
          </p>
        )}
      </section>
    </>
  )
}

/**
 * The head of a message under its subject: who sent it, when, who signed it, the details and the actions.
 * `views` is where ADR 0026's fifth pull request puts the "Formatted" / "Text" switch, beside the actions, and
 * the notes about the formatted view go under it.
 */
function MessageFacts({
  message,
  actions,
  views,
}: {
  message: api.Message
  actions: ReactNode
  views?: ReactNode
}) {
  const signed = message.auth.filter((check) => check.result === 'pass')
  return (
    <>
      <div className="mt-4 flex flex-col gap-1 sm:flex-row sm:items-baseline sm:justify-between sm:gap-6">
        <p className="min-w-0 text-body">
          <span className="text-ink-2">From </span>
          {message.from.length === 0 ? (
            <span className="text-ink-2">no sender</span>
          ) : (
            keyed(message.from, (from) => from.address).map(([key, from], i) => (
              <span key={key}>
                {i > 0 && ', '}
                <Who address={from} />
              </span>
            ))
          )}
        </p>
        <time dateTime={message.received_at} className="shrink-0 font-mono text-mono text-ink-2">
          {fullTime(message.received_at)}
        </time>
      </div>
      {signed.length > 0 && (
        <p className="mt-1 text-small text-ink-2">
          {keyed(signed, (check) => check.domain).map(([key, check], i) => (
            <span key={key}>
              {i > 0 && ', '}
              Signed by <span className="font-mono wrap-anywhere">{check.domain}</span>
            </span>
          ))}
        </p>
      )}
      <Details message={message} />
      <div className="mt-4 flex flex-wrap items-center gap-3">
        {actions}
        {views}
      </div>
    </>
  )
}

/**
 * Each item with a key for React made from what it is, numbered when the same comes twice: a message may name
 * one address twice, and a sender chooses what it names.
 */
function keyed<T>(items: T[], name: (item: T) => string): Array<[string, T]> {
  const seen = new Map<string, number>()
  return items.map((item) => {
    const base = name(item)
    const count = seen.get(base) ?? 0
    seen.set(base, count + 1)
    return [`${base}#${count}`, item]
  })
}

/** A name and an address, as the message wrote them, isolated from the text around them. */
function Who({ address }: { address: api.MailAddress }) {
  return address.name ? (
    <>
      <bdi className="font-medium">{address.name}</bdi>{' '}
      <span className="font-mono text-small text-ink-2 wrap-anywhere">
        &lt;<bdi>{address.address}</bdi>&gt;
      </span>
    </>
  ) : (
    <span className="font-mono text-small wrap-anywhere">
      <bdi>{address.address}</bdi>
    </span>
  )
}

/** Recipients and the rest, folded away: a native disclosure, keyboard operable as it is. */
function Details({ message }: { message: api.Message }) {
  const rows: Array<[string, ReactNode]> = []
  const list = (addresses: api.MailAddress[]) =>
    keyed(addresses, (a) => a.address).map(([key, a], i) => (
      <span key={key}>
        {i > 0 && ', '}
        <Who address={a} />
      </span>
    ))
  if (message.to.length) rows.push(['To', list(message.to)])
  if (message.cc.length) rows.push(['Cc', list(message.cc)])
  if (message.reply_to.length) rows.push(['Reply to', list(message.reply_to)])
  if (message.date) rows.push(['Date it says', <bdi key="date">{message.date}</bdi>])
  rows.push(['Kept until', fullTime(message.expires_at)])
  rows.push(['Size', size(message.size)])
  for (const check of message.auth.filter((c) => c.result !== 'pass')) {
    rows.push([
      'Not signed',
      <span key={check.domain}>
        <span className="font-mono">{check.domain}</span>: {check.result}
      </span>,
    ])
  }
  return (
    <details className="group mt-3">
      <summary className="w-fit cursor-pointer text-small text-ink-2 hover:text-ink">
        Details
      </summary>
      <dl className="mt-2 max-w-3xl text-small">
        {rows.map(([label, value]) => (
          <div key={label} className="flex gap-4 border-b border-rule py-1.5">
            <dt className="w-24 shrink-0 text-ink-2">{label}</dt>
            <dd className="min-w-0 flex-1 wrap-anywhere">{value}</dd>
          </div>
        ))}
      </dl>
    </details>
  )
}

/** Each attachment, as a file to download: the server sends it as one, whatever type it says it is. */
function Attachments({ mailboxId, message }: { mailboxId: string; message: api.Message }) {
  return (
    <section aria-labelledby="attachments" className="mt-6 max-w-3xl">
      <h2 id="attachments" className="flex items-center gap-1.5 text-title font-medium">
        <Paperclip aria-hidden size={16} className="text-ink-2" />
        {message.attachments.length === 1
          ? '1 attachment'
          : `${message.attachments.length} attachments`}
      </h2>
      <ul className="mt-2 border-t border-rule">
        {message.attachments.map((attachment) => (
          <li
            key={attachment.index}
            className="flex flex-wrap items-baseline gap-x-4 gap-y-1 border-b border-rule py-2"
          >
            <bdi className="min-w-0 flex-1 basis-48 text-body wrap-anywhere">
              {attachment.name || `Attachment ${attachment.index + 1}`}
            </bdi>
            <span className="font-mono text-mono text-ink-2 wrap-anywhere">
              {attachment.type}, {size(attachment.size)}
            </span>
            <a
              href={api.attachmentUrl(mailboxId, message.id, attachment.index)}
              download
              className={actionClass('ink', 'sm')}
            >
              Download
              <span className="sr-only">
                {' '}
                {attachment.name || `attachment ${attachment.index + 1}`}
              </span>
            </a>
          </li>
        ))}
      </ul>
    </section>
  )
}
