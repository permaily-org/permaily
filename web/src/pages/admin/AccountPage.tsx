import { useCallback, useEffect, useRef, useState } from 'react'
import { useParams } from 'react-router'
import { Button, SectionHeading, TextLink } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { closingDay, closingDays, formatFullDay } from '../../lib/closing.ts'
import { providerNames } from '../../lib/providers.ts'
import { SettingRow } from '../settings/rows.tsx'
import { formatDay } from '../settings/signin/shared.ts'
import { stateWords } from './AccountsPage.tsx'
import { MailboxesSection } from './MailboxesSection.tsx'
import {
  AskDialog,
  adminMessage,
  formatWhen,
  ListState,
  useAdmin,
  useAdminChange,
} from './shared.tsx'

/** The language's own name in English, as the browser knows it: "English" for `en`. */
function languageName(tag: string): string {
  try {
    return new Intl.DisplayNames('en', { type: 'language' }).of(tag) ?? tag
  } catch {
    return tag
  }
}

type Change = {
  action: api.AccountAction
  label: string
  title: string
  danger: boolean
  /** What happens, in one sentence. */
  says: (record: api.AccountRecord) => string
  /** Said once it is done. */
  done: string
}

const changes: Record<api.AccountAction, Change> = {
  block: {
    action: 'block',
    label: 'Block account',
    title: 'Block this account?',
    danger: true,
    says: () =>
      'Every device signed in to it is signed out, and nobody can sign in to it until it is unblocked.',
    done: 'Account blocked.',
  },
  unblock: {
    action: 'unblock',
    label: 'Unblock account',
    title: 'Unblock this account?',
    danger: false,
    says: () =>
      'It can be signed in to again. Its devices stay signed out until the person signs in.',
    done: 'Account unblocked.',
  },
  delete: {
    action: 'delete',
    label: 'Delete account',
    title: 'Delete this account?',
    danger: true,
    says: () =>
      `It stops working now and is removed for good in ${closingDays} days, on ${formatFullDay(closingDay(Date.now()))}. Until then the person can sign in and cancel, unless it is blocked.`,
    done: 'Deletion asked for.',
  },
  'delete/cancel': {
    action: 'delete/cancel',
    label: 'Cancel the deletion',
    title: 'Cancel this deletion?',
    danger: false,
    says: () => 'The account is kept, and works as it did before.',
    done: 'Deletion cancelled.',
  },
  'sessions/close': {
    action: 'sessions/close',
    label: 'Sign out every device',
    title: 'Sign out every device?',
    danger: true,
    says: () => 'Every device signed in to it is signed out. The person can sign in again.',
    done: 'Every device was signed out.',
  },
}

/** One account, everything the panel knows of it, and what an admin can do to it. */
export function AccountPage() {
  const { id = '' } = useParams()
  const { failed, superAdmin, self, refreshStats } = useAdmin()
  const change = useAdminChange()
  const [record, setRecord] = useState<api.AccountRecord | null>(null)
  const [failure, setFailure] = useState<{ code: api.ApiErrorCode; text: string } | null>(null)
  const [asking, setAsking] = useState<Change | null>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [said, setSaid] = useState('')
  const actionsRef = useRef<HTMLElement>(null)
  // The action may be gone with what it did: once the question has closed, focus goes to the actions,
  // where what happened is said.
  const toActions = useRef(false)
  useEffect(() => {
    if (asking || !toActions.current) return
    toActions.current = false
    actionsRef.current?.focus()
  }, [asking])

  const load = useCallback(async () => {
    setFailure(null)
    try {
      setRecord(await api.adminAccount(id))
    } catch (problem) {
      const code = failed(problem)
      if (code) setFailure({ code, text: adminMessage(code, 'account') })
    }
  }, [id, failed])

  useEffect(() => {
    setRecord(null)
    void load()
  }, [load])

  async function act() {
    if (!asking || !record || pending) return
    setPending(true)
    setError(null)
    const result = await change(() => api.adminAccountAction(record.id, asking.action), 'account')
    setPending(false)
    if ('done' in result) {
      setRecord(result.done)
      setSaid(asking.done)
      toActions.current = true
      setAsking(null)
      refreshStats()
      return
    }
    setError(result.said)
    // Refused for how the account is now: it is read again so the page says how it is.
    if (result.code === 'not_cancellable' || result.code === 'super_admin_only') void load()
  }

  if (!record) {
    if (failure?.code === 'not_found') {
      return (
        <>
          <h1 className="text-display-s font-medium">Account not found</h1>
          <p className="mt-3 text-body text-ink-2">{failure.text}</p>
          <TextLink className="mt-6" to="/admin/accounts">
            Back to accounts
          </TextLink>
        </>
      )
    }
    return (
      <>
        <h1 className="text-display-s font-medium">Account</h1>
        <div className="mt-6">
          <ListState
            failure={failure?.text ?? null}
            what="This account"
            onRetry={() => void load()}
          />
        </div>
      </>
    )
  }

  const own = record.id === self
  const protectedAdmin = record.admin !== undefined && !superAdmin
  const closing = record.state === 'closing'
  const offered: Change[] =
    own || protectedAdmin
      ? []
      : [
          record.blocked_at ? changes.unblock : changes.block,
          ...(closing
            ? record.closing_by === 'operator'
              ? [changes['delete/cancel']]
              : []
            : [changes.delete]),
          ...(record.sessions > 0 ? [changes['sessions/close']] : []),
        ]
  const closesOn = record.closes_at ? new Date(record.closes_at) : null

  return (
    <>
      <TextLink to="/admin/accounts" size="sm">
        Back to accounts
      </TextLink>
      <h1 className="mt-4 wrap-anywhere font-mono text-title font-medium md:text-display-s">
        {record.address}
      </h1>

      <section aria-labelledby="account-facts" className="mt-8 max-w-3xl">
        <SectionHeading id="account-facts" title="The account" />
        <SettingRow label="Name">{record.name || 'None given'}</SettingRow>
        <SettingRow label="State">
          {stateWords(record)}
          {record.blocked_at && (
            <span className="block text-small text-ink-2">
              Blocked since {formatWhen(record.blocked_at)}
            </span>
          )}
          {closing && closesOn && !Number.isNaN(closesOn.getTime()) && (
            <span className="block text-small text-ink-2">
              Removed on {formatFullDay(closesOn)}.{' '}
              {record.closing_by === 'operator'
                ? 'An admin asked for it.'
                : 'The person asked for it.'}
            </span>
          )}
        </SettingRow>
        <SettingRow label="Created">{formatDay(record.created_at)}</SettingRow>
        <SettingRow label="Address checked">
          {record.address_verified_at ? formatDay(record.address_verified_at) : 'Not yet'}
        </SettingRow>
        <SettingRow label="Language">{languageName(record.language)}</SettingRow>
        <SettingRow label="Passkeys">
          {record.passkeys === 0 ? 'None' : record.passkeys}
          <span className="block text-small text-ink-2">
            Passkey only {record.passkey_only ? 'on' : 'off'}
          </span>
        </SettingRow>
        <SettingRow label="Providers">
          {record.providers.length === 0
            ? 'None linked'
            : record.providers.map((provider) => providerNames[provider]).join(', ')}
        </SettingRow>
        <SettingRow label="Devices">
          {record.sessions === 1 ? '1 signed in' : `${record.sessions} signed in`}
        </SettingRow>
        <SettingRow label="Mailboxes">
          {api.accountMailboxes(record) ?? 'Not said by this server'}
        </SettingRow>
        {record.admin && (
          <SettingRow label="Admin">
            {record.admin === 'super' ? 'Super admin' : 'Admin'}
          </SettingRow>
        )}
        <SettingRow label="Id">
          <span className="wrap-anywhere font-mono text-small">{record.id}</span>
        </SettingRow>
      </section>

      <section
        ref={actionsRef}
        tabIndex={-1}
        aria-labelledby="account-actions"
        className="mt-10 max-w-3xl outline-none"
      >
        <SectionHeading id="account-actions" title="Actions" />
        <p className="mt-3 text-small text-ink-2">
          The admin panel can't change an account's address or how it signs in, and can't sign in as
          its owner.
        </p>
        {own && (
          <p className="mt-3 text-body">
            This is your own account. Change it in <TextLink to="/settings">Settings</TextLink>.
          </p>
        )}
        {protectedAdmin && !own && (
          <p className="mt-3 text-body">Only the super admin can act on an admin's account.</p>
        )}
        {closing && record.closing_by !== 'operator' && !own && !protectedAdmin && (
          <p className="mt-3 text-body">
            The person asked for this deletion: only they can cancel it.
          </p>
        )}
        {offered.length > 0 && (
          <div className="mt-4 flex flex-wrap gap-3">
            {offered.map((one) => (
              <Button
                key={one.action}
                variant={one.danger ? 'danger' : 'secondary'}
                size="sm"
                onClick={() => {
                  setError(null)
                  setAsking(one)
                }}
              >
                {one.label}
              </Button>
            ))}
          </div>
        )}
        <p role="status" className="mt-3 text-small text-ink-2 empty:hidden">
          {said}
        </p>
      </section>

      <MailboxesSection account={record.id} admin={record.admin !== undefined} />

      <AskDialog
        id="account-change"
        open={asking !== null}
        title={asking?.title ?? ''}
        subject={record.address}
        error={error}
        pending={pending}
        action={asking?.label ?? ''}
        danger={asking?.danger}
        onAct={() => void act()}
        onClose={() => setAsking(null)}
      >
        {asking?.says(record)}
      </AskDialog>
    </>
  )
}
