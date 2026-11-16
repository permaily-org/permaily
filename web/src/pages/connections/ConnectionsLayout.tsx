import { NavLink, Outlet } from 'react-router'
import { AppShell } from '../../components/AppShell.tsx'
import { Logo } from '../../components/Logo.tsx'
import { ButtonLink } from '../../components/ui/index.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { useStanding } from '../accounts/standing.ts'

/**
 * The connections routes that an account that has not agreed to the terms in force still reaches (ADR 0030,
 * finding 17): its connections keep reading, so the person can always see them and revoke one or a device of one.
 * `/connections/new` is not one of them.
 */
export function isConnectionsPath(pathname: string): boolean {
  return /^\/connections(\/(?!new\/?$)[^/]+(\/[^/]+)?)?\/?$/.test(pathname)
}

/** True while the connections pages show in their limited form: listing, reading and revoking, never renewing. */
export function useLimitedConnections(): boolean {
  const standing = useStanding()
  return standing.terms && !standing.closing
}

const tab = ({ isActive }: { isActive: boolean }) =>
  cx(
    'rounded-sm px-2.5 py-1.5 text-body',
    isActive ? 'bg-sunken font-medium text-ink' : 'text-ink-2 hover:bg-sunken hover:text-ink',
    focusRing,
  )

/**
 * Around the connections routes: the app's own shell, or, for an account that has not agreed to the terms in force,
 * a limited one that says what it may still do and leads to the agreement.
 */
export function ConnectionsLayout() {
  const limited = useLimitedConnections()
  if (!limited) return <AppShell />
  return (
    <div className="flex min-h-svh flex-col">
      <header className="flex flex-wrap items-center justify-between gap-x-6 gap-y-2 border-b border-rule px-4 py-2.5 sm:px-8 lg:px-14">
        <Logo />
        <nav aria-label="What you can still do" className="flex gap-1">
          <NavLink to="/connections" className={tab}>
            Connections
          </NavLink>
          <NavLink to="/inbox" className={tab}>
            Inbox
          </NavLink>
          <NavLink to="/accounts" end className={tab}>
            Accounts
          </NavLink>
        </nav>
      </header>
      <div className="flex flex-col gap-2 px-4 pt-4 sm:px-8 lg:px-14">
        <section
          aria-label="The terms changed"
          className="flex flex-col items-start gap-3 rounded-sm bg-attention-bg px-3 py-3 text-attention sm:flex-row sm:items-center sm:gap-4"
        >
          <p className="min-w-0 flex-1 font-medium">
            Agree to the new terms to use Permaily. Until then your connections keep reading, and
            you can see and revoke them.
          </p>
          {/* Every route but these and the mailboxes' shows the screen that asks for the agreement. */}
          <ButtonLink to="/" variant="secondary" size="sm">
            Read and agree
          </ButtonLink>
        </section>
      </div>
      <main className="min-w-0 flex-1">
        <Outlet />
      </main>
    </div>
  )
}
