import { createContext, type ReactNode, useContext, useState } from 'react'
import { NavLink, Outlet } from 'react-router'
import { AppShell } from '../../components/AppShell.tsx'
import { Logo } from '../../components/Logo.tsx'
import { Button, ButtonLink } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { closesAt, formatFullDay } from '../../lib/closing.ts'
import { useSession } from '../../lib/session.tsx'
import { cx, focusRing } from '../../lib/styles.ts'
import { messageFor } from '../auth/messages.ts'
import { limited, type Standing, useStanding } from './standing.ts'

/** True inside the limited shell, which draws the page's header and `main` for every page in it. */
const InLimitedShell = createContext(false)

/**
 * Around the mailbox routes: the app's own shell for an account that is open and agreed, and for one that is
 * closing or has not agreed a limited one, with what it may still do and a way out of that state. `flow` is for
 * the full-page set-up, which has no sidebar.
 */
export function MailboxLayout({ flow = false }: { flow?: boolean }) {
  const standing = useStanding()
  if (!limited(standing)) return flow ? <Outlet /> : <AppShell />
  return (
    <InLimitedShell.Provider value>
      <LimitedShell standing={standing}>
        <Outlet />
      </LimitedShell>
    </InLimitedShell.Provider>
  )
}

/**
 * A page of its own, without the sidebar: the wordmark and a way back, then the page in `main`. Inside the
 * limited shell, which has both, it is the page alone.
 */
export function FlowFrame({ children }: { children: ReactNode }) {
  const inShell = useContext(InLimitedShell)
  if (inShell) return <>{children}</>
  return (
    <div className="mx-auto w-full max-w-[1296px] px-4 pt-6 pb-10 sm:px-8 sm:pt-8 sm:pb-16 lg:px-12">
      <header className="flex items-center justify-between gap-6">
        <Logo />
        <NavLink
          to="/accounts"
          className={cx(
            'border-b border-ink pb-px text-small font-medium leading-tight hover:border-b-2 hover:pb-0',
            focusRing,
          )}
        >
          Back to accounts
        </NavLink>
      </header>
      <main>{children}</main>
    </div>
  )
}

const tab = ({ isActive }: { isActive: boolean }) =>
  cx(
    'rounded-sm px-2.5 py-1.5 text-body',
    isActive ? 'bg-sunken font-medium text-ink' : 'text-ink-2 hover:bg-sunken hover:text-ink',
    focusRing,
  )

function LimitedShell({ standing, children }: { standing: Standing; children: ReactNode }) {
  return (
    <div className="flex min-h-svh flex-col">
      <header className="flex flex-wrap items-center justify-between gap-x-6 gap-y-2 border-b border-rule px-4 py-2.5 sm:px-8 lg:px-14">
        <Logo />
        <nav aria-label="Mailboxes" className="flex gap-1">
          <NavLink to="/inbox" className={tab}>
            Inbox
          </NavLink>
          <NavLink to="/accounts" end className={tab}>
            Accounts
          </NavLink>
        </nav>
      </header>
      <div className="flex flex-col gap-2 px-4 pt-4 sm:px-8 lg:px-14">
        {standing.closing && <ClosingBanner closes={standing.closesAt} />}
        {standing.terms && <TermsBanner />}
      </div>
      <main className="min-w-0 flex-1">{children}</main>
    </div>
  )
}

const banner =
  'flex flex-col items-start gap-3 rounded-sm bg-attention-bg px-3 py-3 text-attention sm:flex-row sm:items-center sm:gap-4'

/** The account is being deleted: what it may still do here, and cancelling, which the person can do here. */
function ClosingBanner({ closes }: { closes?: string }) {
  const { accountChanged, failed } = useSession()
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const when = closesAt(closes)

  async function keep() {
    if (pending) return
    setPending(true)
    setError(null)
    try {
      accountChanged(await api.cancelDeletion())
    } catch (failure) {
      setPending(false)
      const code = failed(failure)
      if (code) setError(`The deletion wasn't cancelled. ${messageFor(code)}`)
    }
  }

  return (
    <section aria-label="Your account is being deleted" className={banner}>
      <div className="min-w-0 flex-1">
        <p className="font-medium">
          Your account is being deleted
          {when ? ` on ${formatFullDay(when)}` : ''}. Until then you can read and download your mail
          and disconnect mailboxes.
        </p>
        <p role="alert" className="mt-1 text-small text-danger empty:hidden">
          {error}
        </p>
      </div>
      <Button variant="secondary" size="sm" aria-disabled={pending} onClick={keep}>
        Cancel deletion
      </Button>
    </section>
  )
}

/** The terms changed: what the account may still do here, and the screen that asks for the agreement. */
function TermsBanner() {
  return (
    <section aria-label="The terms changed" className={banner}>
      <p className="min-w-0 flex-1 font-medium">
        Agree to the new terms to use Permaily. Until then you can download your mail and disconnect
        your mailboxes.
      </p>
      {/* Every route but these shows the screen that asks for the agreement. */}
      <ButtonLink to="/" variant="secondary" size="sm">
        Read and agree
      </ButtonLink>
    </section>
  )
}
