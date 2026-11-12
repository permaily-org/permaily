import { type FormEvent, useCallback, useEffect, useRef, useState } from 'react'
import { Link } from 'react-router'
import { Button, SectionHeading, TextAction } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { Field } from '../auth/Field.tsx'
import { formatDay } from '../settings/signin/shared.ts'
import {
  AskDialog,
  adminMessage,
  formatWhen,
  ListState,
  useAdmin,
  useAdminChange,
} from './shared.tsx'

/** A mailbox's state in words, as the admin panel sees it. */
export function mailboxStateWords(record: api.MailboxRecord): string {
  switch (record.state) {
    case 'waiting':
      return 'Waiting for mail'
    case 'receiving':
      return 'Receiving'
    case 'paused':
      return 'Paused'
    default:
      return 'Unreadable'
  }
}

type Change = {
  action: 'pause' | 'resume'
  label: string
  title: string
  says: string
  done: string
}

const changes: Record<Change['action'], Change> = {
  pause: {
    action: 'pause',
    label: 'Pause mailbox',
    title: 'Pause this mailbox?',
    says: 'It refuses new mail until it is resumed. The mail it holds stays, and its owner can still read, delete and disconnect it. They are told by email.',
    done: 'Mailbox paused.',
  },
  resume: {
    action: 'resume',
    label: 'Resume mailbox',
    title: 'Resume this mailbox?',
    says: 'It accepts mail again. Its owner is told by email.',
    done: 'Mailbox resumed.',
  },
}

/** One mailbox: what its signed record says, and what an admin may do to it. */
function MailboxRow({
  record,
  acts,
  onAsk,
}: {
  record: api.MailboxRecord
  acts: boolean
  onAsk: (record: api.MailboxRecord, change: Change) => void
}) {
  const unreadable = record.state === 'unreadable'
  const change = record.state === 'paused' ? changes.resume : changes.pause
  return (
    <li className="flex flex-col gap-1 border-b border-rule py-2 sm:flex-row sm:items-center sm:gap-4">
      <span className="min-w-0 flex-1">
        <span className="block wrap-anywhere font-mono text-small">{record.id}</span>
        <span className="block text-small text-ink-2">
          {mailboxStateWords(record)}
          {record.paused_at && <> since {formatWhen(record.paused_at)}</>}
          {unreadable
            ? ". What Permaily stores for it can't be read: it accepts no mail and nobody can open it."
            : `. Keeps mail ${record.retention_days} days.${record.verified ? ' Google confirmed its Gmail account.' : ''}`}
        </span>
        <span className="block text-small text-ink-2">
          Added {formatDay(record.created_at)}
          {record.receiving_since && <>, receiving since {formatDay(record.receiving_since)}</>}
        </span>
      </span>
      {acts && !unreadable && (
        <Button
          variant={change.action === 'pause' ? 'danger' : 'secondary'}
          size="sm"
          className="self-start sm:self-auto"
          onClick={() => onAsk(record, change)}
        >
          {change.label}
        </Button>
      )}
    </li>
  )
}

/**
 * An account's mailboxes as the admin panel sees them: never an address, a label or mail. An admin pauses one
 * to stop abuse, and resumes it; the super admin alone does that to an admin's.
 */
export function MailboxesSection({ account, admin }: { account: string; admin: boolean }) {
  const { failed, superAdmin, refreshStats } = useAdmin()
  const change = useAdminChange()
  const [records, setRecords] = useState<api.MailboxRecord[] | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [asking, setAsking] = useState<{ record: api.MailboxRecord; change: Change } | null>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [said, setSaid] = useState('')
  const sectionRef = useRef<HTMLElement>(null)
  const toSection = useRef(false)
  useEffect(() => {
    if (asking || !toSection.current) return
    toSection.current = false
    sectionRef.current?.focus()
  }, [asking])

  const load = useCallback(async () => {
    setFailure(null)
    try {
      setRecords(await api.adminAccountMailboxes(account))
    } catch (problem) {
      const code = failed(problem)
      if (code) setFailure(adminMessage(code, 'mailbox'))
    }
  }, [account, failed])

  useEffect(() => {
    setRecords(null)
    void load()
  }, [load])

  async function act() {
    if (!asking || pending) return
    setPending(true)
    setError(null)
    const result = await change(
      () => api.adminMailboxAction(asking.record.id, asking.change.action),
      'mailbox',
    )
    setPending(false)
    if ('done' in result) {
      const after = result.done
      setRecords((now) => (now ?? []).map((one) => (one.id === after.id ? after : one)))
      setSaid(asking.change.done)
      toSection.current = true
      setAsking(null)
      refreshStats()
      return
    }
    setError(result.said)
    // Refused for how the mailbox is now: it is read again so the list says how it is.
    if (result.code === 'not_found' || result.code === 'super_admin_only') void load()
  }

  const acts = superAdmin || !admin
  return (
    <section
      ref={sectionRef}
      tabIndex={-1}
      aria-labelledby="account-mailboxes"
      className="mt-10 max-w-3xl outline-none"
    >
      <SectionHeading id="account-mailboxes" title="Mailboxes" />
      <p className="mt-3 text-small text-ink-2">
        The admin panel can't read a mailbox, see its address or label, disconnect it or delete its
        mail.
      </p>
      {!acts && (
        <p className="mt-3 text-body">
          Only the super admin can pause or resume an admin's mailbox.
        </p>
      )}
      <div className="mt-4">
        {records === null ? (
          <ListState failure={failure} what="The mailboxes" onRetry={() => void load()} />
        ) : records.length === 0 ? (
          <p className="border-b border-rule py-2 text-small text-ink-2">
            This account has no mailboxes.
          </p>
        ) : (
          <ul aria-labelledby="account-mailboxes">
            {records.map((record) => (
              <MailboxRow
                key={record.id}
                record={record}
                acts={acts}
                onAsk={(one, what) => {
                  setError(null)
                  setAsking({ record: one, change: what })
                }}
              />
            ))}
          </ul>
        )}
      </div>
      <p role="status" className="mt-3 text-small text-ink-2 empty:hidden">
        {said}
      </p>

      <AskDialog
        id="mailbox-change"
        open={asking !== null}
        title={asking?.change.title ?? ''}
        subject={asking?.record.id}
        error={error}
        pending={pending}
        action={asking?.change.label ?? ''}
        danger={asking?.change.action === 'pause'}
        onAct={() => void act()}
        onClose={() => setAsking(null)}
      >
        {asking?.change.says}
      </AskDialog>
    </section>
  )
}

/**
 * Finds the mailbox of an address someone reported, and whose it is. The address typed stays in this page's
 * memory, is never put in the address bar, and is not said back: an address that leads to no mailbox answers
 * the same whatever it is.
 */
export function FindMailbox() {
  const { failed } = useAdmin()
  const [typed, setTyped] = useState('')
  const [typedError, setTypedError] = useState<string | null>(null)
  const [found, setFound] = useState<{ record: api.MailboxRecord | null } | 'finding' | null>(null)
  const [findFailure, setFindFailure] = useState<string | null>(null)
  const fieldRef = useRef<HTMLInputElement>(null)

  async function find(event: FormEvent) {
    event.preventDefault()
    if (found === 'finding') return
    const address = typed.trim()
    if (!address.includes('@')) {
      setTypedError('Enter the whole address of the mailbox.')
      fieldRef.current?.focus()
      return
    }
    setTypedError(null)
    setFindFailure(null)
    setFound('finding')
    try {
      setFound({ record: await api.adminFindMailbox(address) })
    } catch (error) {
      setFound(null)
      const code = failed(error)
      if (code) setFindFailure(adminMessage(code, 'mailbox'))
    }
  }

  return (
    <section aria-labelledby="find-mailbox" className="mt-10">
      <SectionHeading id="find-mailbox" title="Find a mailbox" />
      <form noValidate onSubmit={find} className="mt-4 flex max-w-xl flex-col gap-4">
        <Field
          id="find-mailbox-address"
          label="Its address, exactly"
          type="email"
          autoComplete="off"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          inputRef={fieldRef}
          value={typed}
          onChange={(event) => setTyped(event.target.value)}
          error={typedError}
        />
        <div>
          <Button type="submit" variant="secondary" aria-disabled={found === 'finding'}>
            Find
          </Button>
        </div>
      </form>
      <div role="status" className="mt-4 empty:hidden">
        {found === 'finding' ? (
          <p className="text-small text-ink-2">Finding</p>
        ) : found ? (
          found.record ? (
            <dl aria-label="Mailbox found" className="max-w-xl">
              <div className="flex flex-col gap-0.5 border-b border-rule py-2 sm:flex-row sm:gap-4">
                <dt className="text-small text-ink-2 sm:w-[116px]">Mailbox</dt>
                <dd className="wrap-anywhere font-mono text-small">{found.record.id}</dd>
              </div>
              <div className="flex flex-col gap-0.5 border-b border-rule py-2 sm:flex-row sm:gap-4">
                <dt className="text-small text-ink-2 sm:w-[116px]">State</dt>
                <dd className="text-small">{mailboxStateWords(found.record)}</dd>
              </div>
              <div className="flex flex-col gap-0.5 border-b border-rule py-2 sm:flex-row sm:gap-4">
                <dt className="text-small text-ink-2 sm:w-[116px]">Account</dt>
                <dd>
                  <Link
                    to={`/admin/accounts/${encodeURIComponent(found.record.account)}`}
                    className={cx(
                      'wrap-anywhere font-mono text-small text-ink underline underline-offset-4',
                      focusRing,
                    )}
                  >
                    {found.record.account}
                  </Link>
                </dd>
              </div>
            </dl>
          ) : (
            <p className="text-small text-ink-2">No mailbox has that address.</p>
          )
        ) : null}
      </div>
      <p role="alert" className="mt-4 text-small text-danger empty:hidden">
        {findFailure ? `Couldn't look for it. ${findFailure}` : ''}
      </p>
      {found !== null && found !== 'finding' && (
        <TextAction
          className="mt-3"
          onClick={() => {
            setFound(null)
            setTyped('')
            fieldRef.current?.focus()
          }}
        >
          Clear
        </TextAction>
      )}
    </section>
  )
}
