import { Navigate, Outlet, useLocation } from 'react-router'
import { useSession } from '../lib/session.tsx'
import { AuthLayout } from '../pages/auth/AuthLayout.tsx'
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
  // A sign-in link takes a moment and says so. Asking who is signed in is too quick to name.
  const link = state.status === 'loading' && state.link
  return (
    <AuthLayout title={link ? 'Signing you in' : 'Loading'}>
      <p role="status" className={link ? 'text-body text-ink-2' : 'sr-only'}>
        {link ? 'Signing you in' : 'Loading'}
      </p>
    </AuthLayout>
  )
}

/** Everything of the app sits behind this: without a session, every route leads to the sign-in screen. */
export function RequireSession() {
  const { state } = useSession()
  const { pathname, search, hash } = useLocation()
  if (state.status === 'signedIn') return <Outlet />
  if (state.status === 'signedOut') {
    // After signing in the person lands where they were going, unless they just chose to leave.
    const from = state.byChoice ? null : { from: pathname + search + hash }
    return <Navigate to="/sign-in" replace state={from} />
  }
  return <Waiting />
}

/** The sign-in and sign-up screens: someone already signed in goes on to the app. */
export function GuestOnly() {
  const { state } = useSession()
  const location = useLocation()
  if (state.status === 'signedIn') return <Navigate to={destination(location.state)} replace />
  if (state.status === 'signedOut') return <Outlet />
  return <Waiting />
}
