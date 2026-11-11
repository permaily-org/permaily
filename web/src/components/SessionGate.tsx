import { useRef, useState } from 'react'
import { Navigate, Outlet, useLocation } from 'react-router'
import { isClosing, useSession } from '../lib/session.tsx'
import { AccountClosing } from '../pages/auth/AccountClosing.tsx'
import { AuthLayout } from '../pages/auth/AuthLayout.tsx'
import { LinkConfirmation, LinkEnded } from '../pages/auth/LinkConfirmation.tsx'
import { Button, Headline } from './ui/index.ts'

/** Where a person was going when they were sent to sign in. Only a path inside the app counts. */
function destination(state: unknown): string {
  const from = (state as { from?: unknown } | null)?.from
  if (typeof from !== 'string' || !from.startsWith('/') || from.startsWith('//')) return '/'
  return /^\/sign-(in|up)(?![^/?#])/.test(from) ? '/' : from
}

/** While the server is asked who this is, and when it does not answer. */
function Waiting() {
  const { state, retry } = useSession()
  if (state.status === 'unreachable') {
    return (
      <AuthLayout title="Can't reach Permaily">
        <div role="alert">
          <Headline size="display-s">Permaily can't be reached</Headline>
          <p className="mt-3 text-body text-ink-2">Check your connection, then try again.</p>
        </div>
        <Button className="mt-8" onClick={retry}>
          Try again
        </Button>
      </AuthLayout>
    )
  }
  // Asking whose sign-in a link is takes a moment and says so. Asking who is signed in is too quick to name.
  const link = state.status === 'loading' && state.link
  return (
    <AuthLayout title={link ? 'Checking your link' : 'Loading'}>
      <p role="status" className={link ? 'text-body text-ink-2' : 'sr-only'}>
        {link ? 'Checking your link' : 'Loading'}
      </p>
    </AuthLayout>
  )
}

/**
 * Everything of the app sits behind this: without a session, every route leads to the sign-in screen. And
 * with an account that is being deleted every route shows that, and nothing else of the app, until the person
 * cancels. The address stays where it was, so cancelling goes back to the page they were on.
 */
export function RequireSession() {
  const { state } = useSession()
  const { pathname, search, hash } = useLocation()
  // Said to a screen reader when the app comes back after a deletion was cancelled: it is here, and not on
  // the screen that goes away, so it is still in the page when it is read.
  const [said, setSaid] = useState('')
  if (state.status === 'signedIn') {
    const closing = isClosing(state.me.account)
    return (
      <>
        <p role="status" className="sr-only">
          {closing ? '' : said}
        </p>
        {closing ? (
          <AccountClosing
            account={state.me.account}
            onKept={() => setSaid('Deletion cancelled. Your account works again.')}
          />
        ) : (
          <Outlet />
        )}
      </>
    )
  }
  if (state.status === 'signedOut') {
    // After signing in the person lands where they were going, unless they just chose to leave.
    const from = state.byChoice ? null : { from: pathname + search + hash }
    return <Navigate to="/sign-in" replace state={from} />
  }
  return <Waiting />
}

/**
 * The sign-in and sign-up screens: someone already signed in goes on to the app. A sign-in link comes first,
 * whoever is signed in: it waits here for the person to say whether to sign in with it.
 */
export function GuestOnly() {
  const { state, linkAsk, linkProblem } = useSession()
  const location = useLocation()
  // A link pasted over the sign-in screen makes a new entry in the history, without the router's state:
  // where the person was going is remembered here for as long as these screens stay open.
  const going = useRef<unknown>(null)
  if (location.state) going.current = location.state
  if (state.status === 'loading' || state.status === 'unreachable') return <Waiting />
  if (linkAsk) return <LinkConfirmation />
  if (state.status === 'signedOut') return <Outlet />
  // Only a link the person said yes to leaves a problem behind while signed in: it is told before the app opens.
  if (linkProblem) return <LinkEnded />
  return <Navigate to={destination(location.state ?? going.current)} replace />
}
