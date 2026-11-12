import { useEffect, useRef, useState } from 'react'
import { Button, TextLink } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { closesAt, closingDays, daysLeft, formatClock, formatFullDay } from '../../lib/closing.ts'
import { useSession } from '../../lib/session.tsx'
import { AuthLayout } from './AuthLayout.tsx'
import { Address, FocusedHeadline } from './LinkConfirmation.tsx'
import { messageFor } from './messages.ts'

const title = 'Your account is being deleted'

/** How often the screen looks at the clock: the days left change, and the day itself comes. */
const tick = 60_000

/**
 * All that an account shows from the moment its deletion was asked for: that it is closing, on which day,
 * who asked when it was Permaily's operator and not the person, and the two things left to do, cancel or
 * sign out. Every route leads here until it is cancelled, which the person can do whoever asked.
 */
export function AccountClosing({ account }: { account: api.Account }) {
  const { accountChanged, refresh, failed, signOut } = useSession()
  const [pending, setPending] = useState<'keep' | 'leave' | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [now, setNow] = useState(() => Date.now())
  const headingRef = useRef<HTMLHeadingElement>(null)
  const keepRef = useRef<HTMLButtonElement>(null)
  const leaveRef = useRef<HTMLButtonElement>(null)
  // The action that was tried last: it takes focus back when the try failed.
  const tried = useRef(keepRef)
  const when = closesAt(account.closes_at)
  // A server from before says nothing of who asked: that reads as the person, like anything but `operator`.
  const byOperator = account.closing_by === 'operator'
  const due = when?.getTime()

  // The screen appears by itself, so its headline takes focus: a screen reader starts there.
  useEffect(() => {
    headingRef.current?.focus()
  }, [])

  // After a try that failed, focus goes back to the action, once it can be used again.
  useEffect(() => {
    if (error) tried.current.current?.focus()
  }, [error])

  // The days left are counted again as time passes. Once the day has come the server is asked, and its
  // answer, that there is no session, leads to the sign-in screen: this clock alone signs nobody out.
  useEffect(() => {
    const look = () => {
      const time = Date.now()
      setNow(time)
      if (due !== undefined && time >= due) void refresh()
    }
    const timer = window.setInterval(look, tick)
    return () => window.clearInterval(timer)
  }, [due, refresh])

  async function keep() {
    if (pending) return
    setError(null)
    setPending('keep')
    tried.current = keepRef
    try {
      // The app is back, and RequireSession says so to a screen reader.
      accountChanged(await api.cancelDeletion())
    } catch (failure) {
      // The day came meanwhile: the account is gone, and the sign-in screen follows.
      const code = failed(failure)
      setPending(null)
      if (code) {
        setError(
          code === 'network'
            ? "The deletion wasn't cancelled: Permaily can't be reached. Check your connection, then try again."
            : `The deletion wasn't cancelled. ${messageFor(code)}`,
        )
      }
    }
  }

  async function leave() {
    if (pending) return
    setError(null)
    setPending('leave')
    tried.current = leaveRef
    try {
      await signOut()
    } catch (failure) {
      setPending(null)
      setError(
        api.errorCode(failure) === 'network'
          ? "Couldn't sign out: Permaily can't be reached. Check your connection, then try again."
          : "Couldn't sign out. Try again in a moment.",
      )
    }
  }

  return (
    <AuthLayout title={title}>
      <FocusedHeadline headingRef={headingRef} describedBy="closing-when">
        {title}
      </FocusedHeadline>
      <div className="mt-3 flex flex-col gap-3 text-body text-ink-2">
        <p>
          The account of <Address>{account.address}</Address> has stopped working.
        </p>
        <p id="closing-when">
          {byOperator
            ? "Permaily's operator asked for this account to be deleted on"
            : 'It will be deleted for good on'}{' '}
          <span className="mt-3 block border-y border-rule py-3 text-body text-ink">
            {when ? (
              <>
                <time dateTime={when.toISOString()} className="block font-medium">
                  {formatFullDay(when)}
                </time>
                <span className="block text-small text-ink-2">
                  {formatClock(when)}, {daysLeft(when, now)}
                </span>
              </>
            ) : (
              // The server says the day with every answer. Should one come without it, nothing is made up.
              <span className="block font-medium">a day within the next {closingDays} days</span>
            )}
          </span>
        </p>
        <p>Until then you can cancel, and the account works again as it was.</p>
        <p>
          After that day nothing can bring it back. This address can only return with a new
          invitation, as a new account.
        </p>
      </div>

      <p role="alert" className="mt-5 text-small text-danger empty:hidden">
        {error}
      </p>
      <p role="status" className="sr-only">
        {pending === 'keep' ? 'Cancelling the deletion' : pending === 'leave' ? 'Signing out' : ''}
      </p>
      <div className="mt-8 flex flex-wrap gap-3">
        <Button ref={keepRef} className="flex-1" disabled={pending !== null} onClick={keep}>
          Cancel the deletion
        </Button>
        <Button
          ref={leaveRef}
          variant="secondary"
          className="flex-1"
          disabled={pending !== null}
          onClick={leave}
        >
          Sign out
        </Button>
      </div>
      {/* Until the day, the person can still read, download and disconnect their mailboxes (ADR 0026). */}
      <p className="mt-8 text-small text-ink-2">
        <TextLink to="/accounts">Your mailboxes</TextLink>
      </p>
    </AuthLayout>
  )
}
