import { type ReactNode, type RefObject, useEffect, useRef, useState } from 'react'
import { Button } from '../../components/ui/index.ts'
import { errorCode } from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'
import { AuthLayout } from './AuthLayout.tsx'
import { linkHeadlines, messageFor } from './messages.ts'

/** The headline of a screen that appears by itself: it takes focus, so a screen reader starts there. */
function FocusedHeadline({
  headingRef,
  describedBy,
  children,
}: {
  headingRef: RefObject<HTMLHeadingElement | null>
  describedBy?: string
  children: ReactNode
}) {
  return (
    <h1
      ref={headingRef}
      tabIndex={-1}
      aria-describedby={describedBy}
      className="text-heading font-medium outline-none md:text-display-s"
    >
      {children}
    </h1>
  )
}

/** An address, as a machine fact that may be longer than the column. */
function Address({ children }: { children: string }) {
  return <span className="wrap-anywhere font-mono text-small text-ink">{children}</span>
}

/**
 * What a sign-in link opens. The link signs nobody in by itself: someone may have started a sign-in for their
 * own account and sent the link, to have this person use that account and connect their mail there. The screen
 * names the address the server gave and waits. Neither action is styled as the one to take.
 */
export function LinkConfirmation() {
  const { state, linkAsk, acceptLink, declineLink } = useSession()
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const headingRef = useRef<HTMLHeadingElement>(null)
  const acceptRef = useRef<HTMLButtonElement>(null)

  // Focus goes to the question, not to an action: Enter must not sign anyone in unread.
  useEffect(() => {
    headingRef.current?.focus()
  }, [])

  // After a try that got no answer, focus goes back to the action, once it can be used again.
  useEffect(() => {
    if (error) acceptRef.current?.focus()
  }, [error])

  if (!linkAsk) return null
  // The account this browser is in now. It is another one, or there would be nothing to ask.
  const current = state.status === 'signedIn' ? state.me.account.address : null
  const title = current ? 'Switch to this account?' : 'Sign in to this account?'

  async function accept() {
    if (pending) return
    setError(null)
    setPending(true)
    try {
      await acceptLink()
    } catch (failure) {
      // Only no answer comes back here. The link is kept, so the same question can be answered again.
      setPending(false)
      setError(messageFor(errorCode(failure)))
    }
  }

  return (
    <AuthLayout title={title}>
      <FocusedHeadline headingRef={headingRef} describedBy="link-whose">
        {title}
      </FocusedHeadline>
      <div className="mt-3 flex flex-col gap-3 text-body text-ink-2">
        {current && (
          <p>
            This browser is signed in as <Address>{current}</Address>.
          </p>
        )}
        <p id="link-whose">
          {current
            ? 'This link signs it in to another account, the one of'
            : 'This link signs this browser in to the account of'}
          <span className="mt-3 block border-y border-rule py-3 wrap-anywhere font-mono text-body text-ink">
            {linkAsk.address}
          </span>
        </p>
        <p>
          {current
            ? 'Go on only if that address is yours too and you asked for this message.'
            : 'Go on only if that address is yours and you asked for this message.'}
        </p>
        <p>
          {current
            ? "If someone sent you this link, don't switch. You would be in their account, and a mailbox you connect there would be theirs to read."
            : "If someone sent you this link, don't sign in. You would be in their account, and a mailbox you connect there would be theirs to read."}
        </p>
        {current && (
          <p>
            If you don't switch, this browser stays signed in as <Address>{current}</Address>.
          </p>
        )}
      </div>

      <p role="alert" className="mt-5 text-small text-danger empty:hidden">
        {error}
      </p>
      <p role="status" className="sr-only">
        {pending ? 'Signing you in' : ''}
      </p>
      {/* Two actions of the same weight: this screen is for stopping to read, not for getting through. */}
      <div className="mt-8 flex flex-wrap gap-3">
        <Button
          ref={acceptRef}
          variant="secondary"
          className="flex-1"
          disabled={pending}
          onClick={accept}
        >
          {current ? 'Switch account' : 'Sign in'}
        </Button>
        <Button variant="secondary" className="flex-1" disabled={pending} onClick={declineLink}>
          {current ? "Don't switch" : "Don't sign in"}
        </Button>
      </div>
    </AuthLayout>
  )
}

/**
 * The person said yes, the link had ended meanwhile, and this browser is in another account: it says so
 * instead of opening the app as if the link had worked.
 */
export function LinkEnded() {
  const { state, linkProblem, clearLinkProblem } = useSession()
  const headingRef = useRef<HTMLHeadingElement>(null)

  useEffect(() => {
    headingRef.current?.focus()
  }, [])

  if (!linkProblem || state.status !== 'signedIn') return null
  const title = linkHeadlines[linkProblem]
  return (
    <AuthLayout title={title}>
      <FocusedHeadline headingRef={headingRef} describedBy="link-ended">
        {title}
      </FocusedHeadline>
      <p id="link-ended" className="mt-3 text-body text-ink-2">
        {linkProblem === 'expired' ? 'A link works once, and for a short time. ' : ''}
        This browser is still signed in as <Address>{state.me.account.address}</Address>.
      </p>
      <Button className="mt-8" onClick={clearLinkProblem}>
        Open Permaily
      </Button>
    </AuthLayout>
  )
}
