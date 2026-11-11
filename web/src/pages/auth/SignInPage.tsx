import { type FormEvent, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router'
import { Button, Headline, InfoTip, TextAction, TextLink } from '../../components/ui/index.ts'
import { errorCode, finishEmail, startEmail } from '../../lib/api.ts'
import { arrivedInvitation, dropInvitation } from '../../lib/arrival.ts'
import {
  caretAfter,
  charactersBefore,
  formatInvitation,
  normalizeInvitation,
} from '../../lib/invitation.ts'
import { useSession } from '../../lib/session.tsx'
import { passkeysSupported } from '../../lib/webauthn.ts'
import { AuthLayout } from './AuthLayout.tsx'
import { Field } from './Field.tsx'
import { linkHeadlines, messageFor } from './messages.ts'
import { PasskeySignIn } from './PasskeySignIn.tsx'
import { RecoveryStep } from './RecoveryStep.tsx'

type Mode = 'sign-in' | 'sign-up'

/** A sign-in the server has started: the code is entered against `attempt`. */
type Started = {
  attempt: string
  address: string
  /** The invitation it was started with, kept in memory to ask for another message. */
  invitation: string | null
  /** When the code and the link stop working, in the browser's clock. */
  expiresAt: number
  minutes: number
}

const codeDigits = 6

/**
 * Signing in and signing up are one flow: give an address (and, to sign up, an invitation), then end it with
 * the code of the message, or by opening its link and saying yes to what it asks (LinkConfirmation.tsx).
 * Signing in also takes a passkey, which needs no address (PasskeySignIn.tsx), and for someone who lost
 * theirs, a recovery code (RecoveryStep.tsx). Nothing typed here is kept beyond this page's memory.
 */
export function SignInPage({ mode }: { mode: Mode }) {
  const [started, setStarted] = useState<Started | null>(null)
  const [address, setAddress] = useState('')
  const [invitationError, setInvitationError] = useState<string | null>(null)
  const [recovering, setRecovering] = useState(false)

  if (recovering && mode === 'sign-in') {
    return (
      <RecoveryStep address={address} onAddress={setAddress} onBack={() => setRecovering(false)} />
    )
  }
  if (started) {
    return (
      <CodeStep
        mode={mode}
        started={started}
        onReplaced={setStarted}
        onBack={() => setStarted(null)}
        onInvitationRefused={(message) => {
          setInvitationError(message)
          setStarted(null)
        }}
      />
    )
  }
  return (
    <AddressStep
      mode={mode}
      address={address}
      onAddress={setAddress}
      invitationError={invitationError}
      onStarted={(next) => {
        setInvitationError(null)
        setStarted(next)
      }}
      onRecover={() => setRecovering(true)}
    />
  )
}

async function start(address: string, invitation: string | null): Promise<Started> {
  const answer = await startEmail(address, invitation ?? undefined)
  return {
    attempt: answer.attempt,
    address,
    invitation,
    expiresAt: Date.now() + answer.expires_in * 1000,
    minutes: Math.max(1, Math.round(answer.expires_in / 60)),
  }
}

function AddressStep({
  mode,
  address,
  onAddress,
  invitationError,
  onStarted,
  onRecover,
}: {
  mode: Mode
  address: string
  onAddress: (address: string) => void
  invitationError: string | null
  onStarted: (started: Started) => void
  onRecover: () => void
}) {
  const { linkProblem, clearLinkProblem } = useSession()
  const { state } = useLocation()
  const signUp = mode === 'sign-up'
  const [invitation, setInvitation] = useState(() =>
    signUp ? formatInvitation(arrivedInvitation() ?? '') : '',
  )
  const [errors, setErrors] = useState<{ address?: string; invitation?: string; form?: string }>(
    () => (invitationError ? { invitation: invitationError } : {}),
  )
  const [pending, setPending] = useState(false)
  // A browser without passkeys is not offered one.
  const [passkeys] = useState(passkeysSupported)
  const addressRef = useRef<HTMLInputElement>(null)
  const invitationRef = useRef<HTMLInputElement>(null)
  // Where the caret goes once the invitation is written in groups again.
  const caret = useRef<number | null>(null)

  // The first thing to fill in takes focus: the invitation when it was refused, the address otherwise.
  // biome-ignore lint/correctness/useExhaustiveDependencies: only when the step opens
  useEffect(() => {
    if (invitationError) invitationRef.current?.focus()
    else addressRef.current?.focus()
  }, [])

  // biome-ignore lint/correctness/useExhaustiveDependencies: runs after each change of the invitation
  useLayoutEffect(() => {
    if (caret.current === null) return
    invitationRef.current?.setSelectionRange(caret.current, caret.current)
    caret.current = null
  }, [invitation])

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (pending) return
    const typed = address.trim()
    if (!typed.includes('@')) {
      setErrors({ address: 'Enter your email address.' })
      addressRef.current?.focus()
      return
    }
    const code = signUp ? normalizeInvitation(invitation) : null
    if (signUp && !code) {
      setErrors({
        invitation: 'An invitation has 12 letters and digits. Check it against the one you got.',
      })
      invitationRef.current?.focus()
      return
    }
    setErrors({})
    setPending(true)
    try {
      const next = await start(typed, code)
      clearLinkProblem()
      onStarted(next)
    } catch (error) {
      setPending(false)
      const failure = errorCode(error)
      if (failure === 'invalid_address') {
        setErrors({ address: messageFor(failure) })
        addressRef.current?.focus()
      } else if (failure === 'invalid_invitation') {
        setErrors({ invitation: messageFor(failure) })
        invitationRef.current?.focus()
      } else {
        setErrors({ form: messageFor(failure) })
      }
    }
  }

  const problem = signUp ? null : linkProblem
  const title = problem ? linkHeadlines[problem] : signUp ? 'Create your account' : 'Sign in'

  return (
    <AuthLayout title={title}>
      <Headline size="display-s">{title}</Headline>
      {problem && (
        <p id="link-problem" className="mt-3 text-body text-ink-2">
          {problem === 'expired'
            ? 'A link works once, and for a short time. Enter your address to ask for a new one.'
            : 'Enter your address to ask for a new one.'}
        </p>
      )}

      <form noValidate onSubmit={submit} className="mt-8 flex flex-col gap-5">
        <Field
          id="address"
          label="Email address"
          type="email"
          name="email"
          autoComplete="email"
          inputMode="email"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          required
          inputRef={addressRef}
          value={address}
          onChange={(event) => onAddress(event.target.value)}
          error={errors.address}
          describedBy={problem ? 'link-problem' : undefined}
        />
        {signUp && (
          <Field
            id="invitation"
            label="Invitation"
            tip={
              <InfoTip label="About invitations" align="start">
                Permaily is open by invitation for now. Each one makes one account.
              </InfoTip>
            }
            type="text"
            name="invitation"
            autoComplete="off"
            autoCapitalize="characters"
            autoCorrect="off"
            spellCheck={false}
            required
            placeholder="XXXX-XXXX-XXXX"
            className="font-mono"
            inputRef={invitationRef}
            value={invitation}
            onChange={(event) => {
              const typed = event.target.value
              const formatted = formatInvitation(typed)
              const position = event.target.selectionStart ?? typed.length
              caret.current = caretAfter(formatted, charactersBefore(typed, position))
              setInvitation(formatted)
            }}
            error={errors.invitation}
          />
        )}
        <p role="alert" className="text-small text-danger empty:hidden">
          {errors.form}
        </p>
        <Button type="submit" disabled={pending}>
          Continue
        </Button>
      </form>
      {!signUp && passkeys && <PasskeySignIn />}

      <div className="mt-8 flex flex-col gap-2 border-t border-rule pt-4 text-small text-ink-2">
        <p className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
          {signUp ? (
            <>
              Already have an account?
              <TextLink to="/sign-in" state={state}>
                Sign in
              </TextLink>
            </>
          ) : (
            <>
              Have an invitation?
              <TextLink to="/sign-up" state={state}>
                Create an account
              </TextLink>
            </>
          )}
        </p>
        {!signUp && (
          <p className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
            Lost your passkeys?
            <TextAction onClick={onRecover}>Use a recovery code</TextAction>
          </p>
        )}
      </div>
    </AuthLayout>
  )
}

function CodeStep({
  mode,
  started,
  onReplaced,
  onBack,
  onInvitationRefused,
}: {
  mode: Mode
  started: Started
  onReplaced: (started: Started) => void
  onBack: () => void
  onInvitationRefused: (message: string) => void
}) {
  const session = useSession()
  const signUp = mode === 'sign-up'
  const [code, setCode] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [formError, setFormError] = useState<string | null>(null)
  const [note, setNote] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const [expired, setExpired] = useState(() => started.expiresAt <= Date.now())
  const codeRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    codeRef.current?.focus()
  }, [])

  // The message says how long it works for. When that passes, the page says so instead of failing later.
  useEffect(() => {
    const left = started.expiresAt - Date.now()
    setExpired(left <= 0)
    if (left <= 0) return
    const timer = window.setTimeout(() => setExpired(true), left)
    return () => window.clearTimeout(timer)
  }, [started.expiresAt])

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (pending) return
    if (code.length !== codeDigits) {
      setError(`Enter the ${codeDigits} digits of the code.`)
      codeRef.current?.focus()
      return
    }
    setError(null)
    setFormError(null)
    setNote(null)
    setPending(true)
    try {
      const who = await finishEmail(started.attempt, { code })
      dropInvitation()
      session.signedIn(who)
    } catch (failure) {
      setPending(false)
      const failed = errorCode(failure)
      if (failed === 'invalid_invitation') {
        onInvitationRefused(messageFor(failed))
      } else if (failed === 'invalid_code') {
        setError(messageFor(failed))
        codeRef.current?.focus()
        codeRef.current?.select()
      } else {
        setFormError(messageFor(failed))
      }
    }
  }

  async function sendAnother() {
    if (pending) return
    setError(null)
    setFormError(null)
    setNote(null)
    setPending(true)
    try {
      onReplaced(await start(started.address, started.invitation))
      setCode('')
      setNote('Asked for another message. Use the code or the link of the newest one.')
    } catch (failure) {
      const failed = errorCode(failure)
      setFormError(
        failed === 'invalid_invitation'
          ? "Another message can't be sent with that invitation right now. Use the one you have, or try again in an hour."
          : messageFor(failed),
      )
    } finally {
      setPending(false)
      codeRef.current?.focus()
    }
  }

  return (
    <AuthLayout title="Check your mail">
      <Headline size="display-s">Check your mail</Headline>
      <p id="code-intro" className="mt-3 text-body text-ink-2">
        {signUp ? 'A message is on its way to ' : 'If '}
        <span className="wrap-anywhere font-mono text-small text-ink">{started.address}</span>
        {signUp ? '.' : ' can sign in, a message is on its way.'}{' '}
        {expired
          ? 'It has expired by now.'
          : `It works for ${started.minutes} ${started.minutes === 1 ? 'minute' : 'minutes'}.`}
      </p>

      <form noValidate onSubmit={submit} className="mt-8 flex flex-col gap-5">
        <Field
          id="code"
          label="Code from the message"
          type="text"
          name="code"
          inputMode="numeric"
          pattern="[0-9]*"
          autoComplete="one-time-code"
          autoCorrect="off"
          spellCheck={false}
          required
          className="font-mono text-title tracking-widest"
          inputRef={codeRef}
          value={code}
          // A pasted code may come with spaces or a dash: only its digits are kept.
          onChange={(event) => setCode(event.target.value.replace(/\D/g, '').slice(0, codeDigits))}
          hint="No code in the message? Open its link on this device instead."
          error={error}
          describedBy="code-intro"
        />
        <p role="alert" className="text-small text-danger empty:hidden">
          {formError}
        </p>
        <Button type="submit" disabled={pending}>
          {signUp ? 'Create account' : 'Sign in'}
        </Button>
      </form>

      <p role="status" className="mt-4 text-small text-ink-2 empty:hidden">
        {note}
      </p>
      <div className="mt-8 flex flex-wrap gap-x-5 gap-y-2 border-t border-rule pt-4">
        <TextAction onClick={sendAnother} disabled={pending}>
          Send another message
        </TextAction>
        <TextAction onClick={onBack} disabled={pending}>
          Use another address
        </TextAction>
      </div>
    </AuthLayout>
  )
}
