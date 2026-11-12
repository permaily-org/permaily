import { type FormEvent, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Navigate, useLocation, useNavigate } from 'react-router'
import { Button, Headline, InfoTip, TextAction, TextLink } from '../../components/ui/index.ts'
import { errorCode, finishEmail, startEmail } from '../../lib/api.ts'
import {
  arrivedInvitation,
  dropInvitation,
  dropProviderArrival,
  useProviderArrival,
} from '../../lib/arrival.ts'
import { formatFullDay } from '../../lib/closing.ts'
import { destination } from '../../lib/destination.ts'
import {
  caretAfter,
  charactersBefore,
  formatInvitation,
  normalizeInvitation,
} from '../../lib/invitation.ts'
import { useProvidersOn } from '../../lib/providers.ts'
import { useSession } from '../../lib/session.tsx'
import { passkeysSupported } from '../../lib/webauthn.ts'
import { AuthLayout } from './AuthLayout.tsx'
import { CodeStep, type Sent, sentTo } from './CodeStep.tsx'
import { Field } from './Field.tsx'
import { Address } from './LinkConfirmation.tsx'
import { linkHeadlines, messageFor, needsInvitation, providerProblem } from './messages.ts'
import { PasskeySignIn } from './PasskeySignIn.tsx'
import { type ConfirmState, ProviderConfirm } from './ProviderConfirm.tsx'
import { ProviderSignIn } from './ProviderSignIn.tsx'
import { RecoveryStep } from './RecoveryStep.tsx'

type Mode = 'sign-in' | 'sign-up'

/** A sign-in by mail the server has started, with the invitation it was started with, to ask for another message. */
type Started = Sent & { invitation: string | null }

const badInvitation = 'An invitation has 12 letters and digits. Check it against the one you got.'

/**
 * Signing in and signing up are one flow: give an address (and, to sign up, an invitation), then end it with
 * the code of the message, or by opening its link and saying yes to what it asks (LinkConfirmation.tsx).
 * Signing in also takes a passkey, which needs no address (PasskeySignIn.tsx), and for someone who lost
 * theirs, a recovery code (RecoveryStep.tsx). Both screens offer the providers that are on
 * (ProviderSignIn.tsx), and this is where a sign-in with one comes back to when it did not sign anyone in:
 * with a reason, or to confirm an address first (ProviderConfirm.tsx). Nothing typed here is kept beyond
 * this page's memory.
 */
export function SignInPage({ mode }: { mode: Mode }) {
  const [started, setStarted] = useState<Started | null>(null)
  const [address, setAddress] = useState('')
  const [invitationError, setInvitationError] = useState<string | null>(null)
  const [recovering, setRecovering] = useState(false)
  const arrived = useProviderArrival()
  const location = useLocation()
  const navigate = useNavigate()

  // A sign-in with a provider that waits for its address. The address bar said so once and was cleaned: from
  // then on the router's state does, which a reload finds again. What waits is always asked of the server.
  const toConfirm =
    mode === 'sign-in' && arrived?.screen === 'sign-in' && arrived.kind === 'confirm'
  const confirming = (location.state as Partial<ConfirmState> | null)?.providerConfirm === true
  useEffect(() => {
    if (!toConfirm) return
    if (confirming) dropProviderArrival('sign-in')
    else {
      const state: ConfirmState = { providerConfirm: true }
      navigate('/sign-in', { replace: true, state })
    }
  }, [toConfirm, confirming, navigate])

  if (mode === 'sign-in' && (toConfirm || confirming)) return <ProviderConfirm />
  // What is about an invitation is said where one is typed.
  if (
    mode === 'sign-in' &&
    arrived?.screen === 'sign-in' &&
    arrived.kind === 'failed' &&
    needsInvitation(arrived.reason)
  ) {
    return <Navigate to="/sign-up" replace state={location.state} />
  }

  if (recovering && mode === 'sign-in') {
    return (
      <RecoveryStep address={address} onAddress={setAddress} onBack={() => setRecovering(false)} />
    )
  }
  if (started) {
    return (
      <MailCodeStep
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
  return { ...sentTo(address, await startEmail(address, invitation ?? undefined)), invitation }
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
  const { linkProblem, clearLinkProblem, deletedOn } = useSession()
  const { state } = useLocation()
  const signUp = mode === 'sign-up'
  // Why a sign-in with a provider came back without signing anyone in, until the person tries something else.
  const arrived = useProviderArrival()
  const failed =
    arrived?.screen === 'sign-in' &&
    arrived.kind === 'failed' &&
    needsInvitation(arrived.reason) === signUp
      ? arrived
      : null
  const reason = failed?.reason ?? null
  const said = failed ? providerProblem(failed.reason, failed.provider) : null
  const [providers, askProviders] = useProvidersOn()
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

  // The first thing to fill in takes focus: the invitation when it was refused or is what is missing, the
  // address otherwise.
  const opened = useRef(false)
  // biome-ignore lint/correctness/useExhaustiveDependencies: when the step opens, and when a provider's answer arrives
  useEffect(() => {
    if (opened.current && !reason) return
    opened.current = true
    if (invitationError || (signUp && reason)) invitationRef.current?.focus()
    else addressRef.current?.focus()
  }, [reason])

  // biome-ignore lint/correctness/useExhaustiveDependencies: runs after each change of the invitation
  useLayoutEffect(() => {
    if (caret.current === null) return
    invitationRef.current?.setSelectionRange(caret.current, caret.current)
    caret.current = null
  }, [invitation])

  /** The invitation to start a provider with: none typed still starts, since the account may exist already. */
  function invitationTyped(): string | null | undefined {
    if (!signUp || !invitation.trim()) return undefined
    const code = normalizeInvitation(invitation)
    if (!code) {
      dropProviderArrival('sign-in')
      setErrors({ invitation: badInvitation })
      invitationRef.current?.focus()
      return null
    }
    setErrors({})
    return code
  }

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (pending) return
    // What a provider's answer said is of no use once the person tries something else.
    dropProviderArrival('sign-in')
    const typed = address.trim()
    if (!typed.includes('@')) {
      setErrors({ address: 'Enter your email address.' })
      addressRef.current?.focus()
      return
    }
    const code = signUp ? normalizeInvitation(invitation) : null
    if (signUp && !code) {
      setErrors({ invitation: badInvitation })
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

  // The newest news comes first: a provider's answer, then a link that did not work.
  const problem = signUp || reason ? null : linkProblem
  // An invitation the server refused is said in its field, like one refused before the browser left.
  const refused = said && reason === 'invalid_invitation' ? said.text : undefined
  const told = said && !refused ? said : null
  // An account seen closing in this browser, whose day has passed: why there is no session to come back to.
  // It names nobody, since whoever reads this may not be whose account it was.
  const deleted = signUp ? null : deletedOn
  const described = [
    problem ? 'link-problem' : told && !signUp ? 'provider-problem' : null,
    deleted ? 'deleted-account' : null,
  ]
    .filter(Boolean)
    .join(' ')
  const title = said
    ? said.title
    : problem
      ? linkHeadlines[problem]
      : signUp
        ? 'Create your account'
        : 'Sign in'

  return (
    <AuthLayout title={title}>
      <Headline size="display-s">{title}</Headline>
      {told && (
        <p id="provider-problem" className="mt-3 text-body text-ink-2">
          {told.text}
        </p>
      )}
      {problem && (
        <p id="link-problem" className="mt-3 text-body text-ink-2">
          {problem === 'expired'
            ? 'A link works once, and for a short time. Enter your address to ask for a new one.'
            : 'Enter your address to ask for a new one.'}
        </p>
      )}
      {deleted && (
        <p id="deleted-account" className="mt-3 text-body text-ink-2">
          An account used in this browser was deleted on{' '}
          <time dateTime={deleted.toISOString()} className="text-ink">
            {formatFullDay(deleted)}
          </time>
          , unless that was cancelled from another device.
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
          describedBy={described || undefined}
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
            error={errors.invitation ?? refused}
            describedBy={told ? 'provider-problem' : undefined}
          />
        )}
        <p role="alert" className="text-small text-danger empty:hidden">
          {errors.form}
        </p>
        <Button type="submit" disabled={pending}>
          Continue
        </Button>
      </form>
      <ProviderSignIn
        first={!signUp && passkeys ? <PasskeySignIn /> : undefined}
        on={providers}
        askAgain={askProviders}
        invitation={signUp ? invitationTyped : undefined}
        onInvitationRefused={(message) => {
          setErrors({ invitation: message })
          invitationRef.current?.focus()
        }}
        returnTo={destination(state)}
      />

      <div className="mt-8 flex flex-col gap-2 border-t border-rule pt-4 text-small text-ink-2">
        <p className="flex flex-wrap items-baseline gap-x-2 gap-y-1">
          {signUp ? (
            <>
              Already have an account?
              <TextLink to="/sign-in" state={state} onClick={() => dropProviderArrival('sign-in')}>
                Sign in
              </TextLink>
            </>
          ) : (
            <>
              Have an invitation?
              <TextLink to="/sign-up" state={state} onClick={() => dropProviderArrival('sign-in')}>
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

/** The code of a sign-in or a sign-up by mail. */
function MailCodeStep({
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
  return (
    <CodeStep
      sent={started}
      intro={
        <>
          {signUp ? 'A message is on its way to ' : 'If '}
          <Address>{started.address}</Address>
          {signUp ? '.' : ' can sign in, a message is on its way.'}
        </>
      }
      hint="No code in the message? Open its link on this device instead."
      submitLabel={signUp ? 'Create account' : 'Sign in'}
      finish={async (code) => {
        const who = await finishEmail(started.attempt, { code })
        dropInvitation()
        session.signedIn(who)
      }}
      ended={(failed) => {
        if (failed !== 'invalid_invitation') return false
        onInvitationRefused(messageFor(failed))
        return true
      }}
      sendAnother={async () => onReplaced(await start(started.address, started.invitation))}
      notSent={(failed) =>
        failed === 'invalid_invitation'
          ? "Another message can't be sent with that invitation right now. Use the one you have, or try again in an hour."
          : undefined
      }
      onBack={onBack}
    />
  )
}
