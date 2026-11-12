import { useEffect, useRef, useState } from 'react'
import { Navigate, Outlet, useLocation } from 'react-router'
import { useProviderArrival } from '../lib/arrival.ts'
import { destination } from '../lib/destination.ts'
import { isClosing, termsToAgree, useSession } from '../lib/session.tsx'
import { AccountClosing } from '../pages/auth/AccountClosing.tsx'
import { AuthLayout } from '../pages/auth/AuthLayout.tsx'
import { LinkConfirmation, LinkEnded } from '../pages/auth/LinkConfirmation.tsx'
import { TermsChanged } from '../pages/auth/Terms.tsx'
import { ConfirmProvider } from './Confirm.tsx'
import { Button, Headline } from './ui/index.ts'

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
 * Everything of the app sits behind this: without a session, every route leads to the sign-in screen. With
 * an account that is being deleted every route shows that, and nothing else of the app, until the person
 * cancels. And once the terms in force are not the ones the person agreed to, every route shows the screen
 * that asks for the agreement, until they agree. The address stays where it was, so the app goes back to the
 * page they were on.
 */
export function RequireSession() {
  const { state } = useSession()
  const { pathname, search, hash } = useLocation()
  // The version of the terms to agree to, when the person has to before anything else.
  const terms = state.status === 'signedIn' ? termsToAgree(state.me) : null
  const blocking =
    state.status !== 'signedIn'
      ? 'waiting'
      : isClosing(state.me.account)
        ? 'closing'
        : terms !== null
          ? 'terms'
          : 'page'
  usePageFocus(blocking, pathname)
  // Said to a screen reader when the app comes back after a deletion was cancelled, or the terms agreed to: it
  // is here, and not on the screen that goes away, so it is still in the page when it is read.
  const [said, setSaid] = useState('')
  const before = useRef(blocking)
  useEffect(() => {
    if (before.current === 'terms' && blocking === 'page')
      setSaid('Thanks. You agreed to the terms.')
    before.current = blocking
  }, [blocking])
  if (state.status === 'signedIn') {
    const closing = blocking === 'closing'
    return (
      <>
        <p role="status" className="sr-only">
          {blocking === 'page' ? said : ''}
        </p>
        {closing ? (
          <AccountClosing
            account={state.me.account}
            onKept={() => setSaid('Deletion cancelled. Your account works again.')}
          />
        ) : terms !== null ? (
          // Deleting the account from there may ask the person to confirm it's them.
          <ConfirmProvider>
            <TermsChanged account={state.me.account} version={terms} />
          </ConfirmProvider>
        ) : (
          // Every page can ask the person to confirm it's them before a change that needs it.
          <ConfirmProvider>
            <Outlet />
          </ConfirmProvider>
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
 * Puts focus on the heading of a page of the app when the page is new to the person: they just signed in,
 * they just cancelled a deletion, or they went to another page. A screen reader then starts at what the page
 * is, not at the sidebar. This is the one place that does it, for every page behind the session.
 *
 * It leaves focus alone when the app opens straight on a page, where the browser starts at the top as on any
 * site, and when something on the page took focus for itself, such as a field that opens ready to type in.
 */
function usePageFocus(showing: 'waiting' | 'closing' | 'terms' | 'page', pathname: string) {
  // What this gate showed last, and whether it ever showed a page.
  const before = useRef<'nothing' | 'waiting' | 'closing' | 'terms' | 'page'>('nothing')
  const shown = useRef(false)
  // biome-ignore lint/correctness/useExhaustiveDependencies: the page is another when its path is
  useEffect(() => {
    const last = before.current
    before.current = showing
    if (showing !== 'page') return
    const first = !shown.current
    shown.current = true
    // The app opened straight on this page: nothing came before it but the wait for the server.
    if (first && last === 'waiting') return
    const page = document.querySelector('main') ?? document.body
    const focused = document.activeElement
    if (focused && focused !== document.body && page.contains(focused)) return
    const heading = page.querySelector('h1')
    if (!heading) return
    // Focusable from here only, never with Tab. It shows no ring: see styles/index.css.
    heading.tabIndex = -1
    heading.focus()
  }, [showing, pathname])
}

/**
 * The sign-in and sign-up screens: someone already signed in goes on to the app. A sign-in link comes first,
 * whoever is signed in: it waits here for the person to say whether to sign in with it.
 */
export function GuestOnly() {
  const { state, linkAsk, linkProblem, recovered } = useSession()
  const arrived = useProviderArrival()
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
  // Two things lead to the sign-in section of Settings instead of where the person was going. A recovery
  // code was just used up for want of a passkey: Settings invites them to add one. And a trip to a provider
  // came back here with nothing done while they are signed in: Settings says that nothing was linked.
  if (recovered || (arrived?.screen === 'settings' && arrived.kind === 'failed')) {
    return <Navigate to="/settings" replace />
  }
  return <Navigate to={destination(location.state ?? going.current)} replace />
}
