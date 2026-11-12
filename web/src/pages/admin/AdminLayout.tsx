import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Navigate, NavLink, Outlet } from 'react-router'
import { ButtonLink, Headline, TextAction } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'
import { cx, focusRing, pagePadding } from '../../lib/styles.ts'
import { messageFor } from '../auth/messages.ts'
import { AdminContext, type AdminPanel } from './shared.tsx'

/**
 * The admin panel, for the accounts whose session says they are admins. Anyone else is led where any route the
 * app does not have leads, and the server is asked nothing.
 */
export function AdminGate() {
  const { state } = useSession()
  if (state.status !== 'signedIn' || !state.me.account.admin) return <Navigate to="/" replace />
  // The session says whether the account signs in with passkeys only: without it the panel asks nothing.
  if (state.me.account.passkey_only !== true) return <PasskeyOnlyNeeded />
  return <AdminLayout superAdmin={state.me.account.admin === 'super'} self={state.me.account.id} />
}

const tabs = [
  { to: '/admin/accounts', label: 'Accounts' },
  { to: '/admin/invitations', label: 'Invitations' },
  { to: '/admin/providers', label: 'Providers' },
  { to: '/admin/admins', label: 'Admins' },
  { to: '/admin/activity', label: 'Activity' },
]

function AdminLayout({ superAdmin, self }: { superAdmin: boolean; self: string }) {
  const session = useSession()
  const [stats, setStats] = useState<api.AdminStatsWithMailboxes | null>(null)
  const [statsFailure, setStatsFailure] = useState<string | null>(null)
  // Whether the panel works: it does only while the admin's own account signs in with passkeys only. The
  // session said so before the panel opened; the server has the last word, on the first request. Until then
  // nothing of the panel asks anything.
  const [standing, setStanding] = useState<'asking' | 'open' | 'passkey-only'>('asking')
  const turn = useRef(0)
  const { failed: sessionFailed, refresh } = session

  const failed = useCallback(
    (error: unknown) => {
      const code = sessionFailed(error)
      if (code === 'passkey_only_needed') {
        // Turned off elsewhere: the session hears it too, so the panel stays shut after this page.
        setStanding('passkey-only')
        void refresh()
        return null
      }
      // The account may no longer be an admin: the session hears it, and the panel goes.
      if (code === 'not_found') void refresh()
      return code
    },
    [sessionFailed, refresh],
  )

  const refreshStats = useCallback(async () => {
    const mine = ++turn.current
    try {
      const answer = await api.adminStatsWithMailboxes()
      if (mine !== turn.current) return
      setStats(answer)
      setStatsFailure(null)
      setStanding('open')
    } catch (error) {
      if (mine !== turn.current) return
      const code = failed(error)
      if (!code) return
      setStatsFailure(messageFor(code))
      // The numbers are not the panel: the rest of it is tried all the same.
      setStanding((now) => (now === 'asking' ? 'open' : now))
    }
  }, [failed])

  useEffect(() => {
    void refreshStats()
  }, [refreshStats])

  const panel = useMemo<AdminPanel>(
    () => ({ superAdmin, self, refreshStats: () => void refreshStats(), failed }),
    [superAdmin, self, refreshStats, failed],
  )

  if (standing === 'passkey-only') return <PasskeyOnlyNeeded />

  return (
    <div className={pagePadding}>
      <nav aria-label="Admin" className="flex flex-wrap gap-x-5 gap-y-2 border-b border-rule pb-3">
        {tabs.map((tab) => (
          <NavLink
            key={tab.to}
            to={tab.to}
            className={({ isActive }) =>
              cx(
                'rounded-sm text-small',
                isActive
                  ? 'font-medium text-ink underline underline-offset-4'
                  : 'text-ink-2 hover:text-ink',
                focusRing,
              )
            }
          >
            {tab.label}
          </NavLink>
        ))}
      </nav>
      <Stats stats={stats} failure={statsFailure} onRetry={() => void refreshStats()} />
      {standing === 'open' ? (
        <AdminContext.Provider value={panel}>
          <div className="mt-8">
            <Outlet />
          </div>
        </AdminContext.Provider>
      ) : (
        <p role="status" className="mt-8 text-small text-ink-2">
          Loading
        </p>
      )}
    </div>
  )
}

function Stats({
  stats,
  failure,
  onRetry,
}: {
  stats: api.AdminStatsWithMailboxes | null
  failure: string | null
  onRetry: () => void
}) {
  if (failure) {
    return (
      <div
        role="alert"
        className="mt-4 flex flex-wrap items-baseline gap-x-3 gap-y-1 text-small text-danger"
      >
        <p>The numbers couldn't load. {failure}</p>
        <TextAction onClick={onRetry}>Try again</TextAction>
      </div>
    )
  }
  if (!stats) return null
  const numbers = [
    { label: 'Accounts', value: stats.accounts.total },
    { label: 'Active', value: stats.accounts.active },
    { label: 'Being deleted', value: stats.accounts.closing },
    { label: 'Blocked', value: stats.accounts.blocked },
    { label: 'Unused invitations', value: stats.invitations.unused },
    ...(stats.mailboxes ? [{ label: 'Mailboxes', value: stats.mailboxes.total }] : []),
  ]
  return (
    <section aria-label="Numbers" className="mt-4">
      <dl className="flex flex-wrap gap-x-8 gap-y-3">
        {numbers.map((number) => (
          <div key={number.label} className="flex flex-col">
            <dt className="text-small text-ink-2">{number.label}</dt>
            <dd className="font-mono text-title">
              {stats.complete ? '' : <span className="sr-only">At least </span>}
              {number.value}
            </dd>
          </div>
        ))}
      </dl>
      {!stats.complete && (
        <p className="mt-2 text-small text-ink-2">
          Some accounts couldn't be read, so each number is at least what it shows.
        </p>
      )}
    </section>
  )
}

/** The one screen of the panel while the admin's own account can still be signed in to with mail. */
function PasskeyOnlyNeeded() {
  return (
    <div className={pagePadding}>
      <Headline size="display-s">Turn on passkey only to use the admin panel</Headline>
      <div className="mt-4 flex max-w-2xl flex-col gap-3 text-body text-ink-2">
        <p>The admin panel works only for accounts that sign in with passkeys only.</p>
        <p>
          While a message to your email address can sign you in, anyone who reads your mail could
          sign in as you and act as an admin. With passkey only on, only your passkeys sign you in.
        </p>
      </div>
      <ButtonLink className="mt-8" to="/settings" state={{ section: 'sign-in' }}>
        Go to sign-in settings
      </ButtonLink>
    </div>
  )
}
