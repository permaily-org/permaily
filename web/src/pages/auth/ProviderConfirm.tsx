import { type FormEvent, useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router'
import { Button, TextAction, TextLink } from '../../components/ui/index.ts'
import {
  type ApiErrorCode,
  errorCode,
  finishProviderConfirm,
  type ProviderPending,
  pendingProvider,
  startProviderConfirm,
} from '../../lib/api.ts'
import { dropInvitation, providerEnded } from '../../lib/arrival.ts'
import { providerNames } from '../../lib/providers.ts'
import { useSession } from '../../lib/session.tsx'
import { AuthLayout } from './AuthLayout.tsx'
import { CodeStep, type Sent, sentTo } from './CodeStep.tsx'
import { Field } from './Field.tsx'
import { Address, FocusedHeadline } from './LinkConfirmation.tsx'
import { messageFor } from './messages.ts'

/** What the server says of the sign-in that waits in this browser. */
type Stage =
  | { kind: 'asking' }
  /** The server gave no answer, or one of its own failures: nobody knows whether anything waits. */
  | { kind: 'unknown'; failure: ApiErrorCode }
  /** Nothing waits: it was confirmed, it ended, or it was started in another browser. */
  | { kind: 'gone' }
  | { kind: 'waiting'; pending: ProviderPending; until: number }

/** The router's state of this screen: it is what a reload finds, and where the person lands afterwards. */
export type ConfirmState = { providerConfirm: true; from?: string }

/**
 * Where a sign-in with a provider lands when the provider did not vouch for an address: it said who the
 * person is there, and not that an address is theirs. The person says which address is theirs and proves it
 * with the code of a message, as signing in by mail does, and only then is the provider linked to that
 * address's account. What waits is the server's, tied to this browser by a cookie the app cannot read, so
 * the screen asks the server, also after a reload. Nothing of it is kept here but that it was asked for.
 */
export function ProviderConfirm() {
  const session = useSession()
  const navigate = useNavigate()
  const [stage, setStage] = useState<Stage>({ kind: 'asking' })
  // Null until the server offers one: what the person typed is kept when they come back from the code.
  const [address, setAddress] = useState<string | null>(null)
  const [sent, setSent] = useState<Sent | null>(null)
  const [errors, setErrors] = useState<{ address?: string; form?: string }>({})
  const [sending, setSending] = useState(false)
  const headingRef = useRef<HTMLHeadingElement>(null)
  const addressRef = useRef<HTMLInputElement>(null)
  const retryRef = useRef<HTMLButtonElement>(null)
  // Counts the questions, so a slower answer to an earlier one cannot undo a later one.
  const turn = useRef(0)
  // The person came back from the code to change the address: the field takes focus, not the headline.
  const changing = useRef(false)

  const ask = useCallback(async () => {
    const mine = ++turn.current
    setStage({ kind: 'asking' })
    try {
      const pending = await pendingProvider()
      if (mine !== turn.current) return
      setAddress((typed) => typed ?? pending.address)
      setStage({ kind: 'waiting', pending, until: Date.now() + pending.expires_in * 1000 })
      // Where the person was going is the server's answer. It rides in the router's state from here, so
      // the app opens there once they are signed in.
      const state: ConfirmState = { providerConfirm: true, from: pending.return_to }
      navigate('/sign-in', { replace: true, state })
    } catch (failure) {
      if (mine !== turn.current) return
      const failed = errorCode(failure)
      setStage(
        failed === 'provider_failed' ? { kind: 'gone' } : { kind: 'unknown', failure: failed },
      )
    }
  }, [navigate])

  useEffect(() => {
    void ask()
  }, [ask])

  // What waits ends by itself. When its time passes, the screen says so instead of failing at the next step.
  const until = stage.kind === 'waiting' ? stage.until : null
  useEffect(() => {
    if (until === null) return
    const timer = window.setTimeout(
      () => {
        turn.current++
        setSent(null)
        setStage({ kind: 'gone' })
      },
      Math.max(0, until - Date.now()),
    )
    return () => window.clearTimeout(timer)
  }, [until])

  // Each screen that appears by itself starts at its headline, so a screen reader starts there and Enter
  // sends nothing unread. A try that got no answer puts focus back on the action that tries again.
  const showing = sent ? 'code' : stage.kind
  const failure = stage.kind === 'unknown' ? stage.failure : null
  const asked = useRef(false)
  // biome-ignore lint/correctness/useExhaustiveDependencies: runs when the screen that shows changes
  useEffect(() => {
    if (showing === 'asking') return
    if (showing === 'unknown') {
      const first = !asked.current
      asked.current = true
      ;(first ? headingRef : retryRef).current?.focus()
      return
    }
    if (showing === 'waiting' && changing.current) {
      changing.current = false
      addressRef.current?.focus()
      addressRef.current?.select()
      return
    }
    if (showing !== 'code') headingRef.current?.focus()
  }, [showing, failure])

  /** Leaves this screen for the ordinary sign-in screen: what waits, if anything, ends by itself. */
  const leave = useCallback(() => {
    turn.current++
    navigate('/sign-in', { replace: true, state: null })
  }, [navigate])

  /** Takes a refusal that ends the confirmation, and says whether it did. */
  function ended(failed: ApiErrorCode): boolean {
    if (failed === 'provider_failed') {
      turn.current++
      setSent(null)
      setStage({ kind: 'gone' })
      return true
    }
    if (
      failed === 'invalid_invitation' ||
      failed === 'provider_linked' ||
      failed === 'provider_in_use' ||
      failed === 'account_closing'
    ) {
      // The address was the person's, and the server would not link the provider to its account: the
      // sign-in screen says why, as it does when a provider's own answer ends that way.
      providerEnded(failed)
      leave()
      return true
    }
    return false
  }

  if (stage.kind === 'asking') {
    return (
      <AuthLayout title="Loading">
        <p role="status" className="sr-only">
          Loading
        </p>
      </AuthLayout>
    )
  }

  if (stage.kind === 'unknown') {
    const title =
      stage.failure === 'network' ? "Permaily can't be reached" : 'Something went wrong on our side'
    return (
      <AuthLayout title={title}>
        <div role="alert">
          <FocusedHeadline headingRef={headingRef}>{title}</FocusedHeadline>
          <p className="mt-3 text-body text-ink-2">
            {stage.failure === 'network'
              ? 'Check your connection, then try again. Your sign-in is still waiting.'
              : stage.failure === 'too_many_requests'
                ? messageFor(stage.failure)
                : 'Try again in a moment.'}
          </p>
        </div>
        <Button ref={retryRef} className="mt-8" onClick={() => void ask()}>
          Try again
        </Button>
      </AuthLayout>
    )
  }

  if (stage.kind === 'gone') {
    const title = 'That sign-in is no longer waiting'
    return (
      <AuthLayout title={title}>
        <FocusedHeadline headingRef={headingRef} describedBy="confirm-gone">
          {title}
        </FocusedHeadline>
        <p id="confirm-gone" className="mt-3 text-body text-ink-2">
          A sign-in waits a short while for its address to be confirmed, and only in the browser
          that started it. Nobody was signed in.
        </p>
        <Button className="mt-8" onClick={leave}>
          Start again
        </Button>
      </AuthLayout>
    )
  }

  const { pending } = stage
  const name = providerNames[pending.provider]

  if (sent) {
    const current = sent
    return (
      <CodeStep
        sent={current}
        intro={
          <>
            If <Address>{current.address}</Address> can sign in, a message is on its way.
          </>
        }
        purpose={`Its code links ${name} to that address's account, and signs you in.`}
        hint={`No code in the message? Its link signs you in, but doesn't link ${name}.`}
        footnote={
          <>
            No message? An address with no Permaily account needs an invitation.{' '}
            <TextLink to="/sign-up">Create an account</TextLink>
          </>
        }
        submitLabel="Confirm and sign in"
        finish={async (code) => {
          const who = await finishProviderConfirm(current.attempt, code)
          dropInvitation()
          // The app opens where the person was going: the router's state says where.
          session.signedIn(who)
        }}
        ended={ended}
        sendAnother={async () => {
          setSent(sentTo(current.address, await startProviderConfirm(current.address)))
        }}
        onBack={() => {
          changing.current = true
          setSent(null)
        }}
      />
    )
  }

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (sending) return
    const typed = (address ?? '').trim()
    if (!typed.includes('@')) {
      setErrors({ address: 'Enter your email address.' })
      addressRef.current?.focus()
      return
    }
    setErrors({})
    setSending(true)
    try {
      setSent(sentTo(typed, await startProviderConfirm(typed)))
    } catch (failure) {
      const failed = errorCode(failure)
      if (ended(failed)) return
      if (failed === 'invalid_address') {
        setErrors({ address: messageFor(failed) })
        addressRef.current?.focus()
      } else {
        setErrors({ form: messageFor(failed) })
      }
    } finally {
      setSending(false)
    }
  }

  const title = 'Confirm your email address'
  return (
    <AuthLayout title={title}>
      <FocusedHeadline headingRef={headingRef} describedBy="confirm-why">
        {title}
      </FocusedHeadline>
      <div id="confirm-why" className="mt-3 flex flex-col gap-3 text-body text-ink-2">
        <p>{name} said who you are there, but not that an email address is yours.</p>
        <p>
          Which address is yours? Permaily sends it a message to check, then links {name} to that
          address's account.
        </p>
      </div>

      <form noValidate onSubmit={submit} className="mt-8 flex flex-col gap-5">
        <Field
          id="confirm-address"
          label="Your email address"
          type="email"
          name="email"
          autoComplete="email"
          inputMode="email"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          required
          inputRef={addressRef}
          value={address ?? ''}
          onChange={(event) => setAddress(event.target.value)}
          hint={pending.address ? `${name} gave this one. Change it if it isn't yours.` : undefined}
          error={errors.address}
        />
        <p role="alert" className="text-small text-danger empty:hidden">
          {errors.form}
        </p>
        <Button type="submit" disabled={sending}>
          Send the message
        </Button>
      </form>

      <div className="mt-8 flex flex-wrap gap-x-5 gap-y-2 border-t border-rule pt-4">
        <TextAction onClick={leave} disabled={sending}>
          Sign in another way
        </TextAction>
      </div>
    </AuthLayout>
  )
}
