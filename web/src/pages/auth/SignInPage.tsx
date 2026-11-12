import { type FormEvent, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Navigate, useLocation, useNavigate } from 'react-router'
import { Button, Headline, InfoTip, TextAction, TextLink } from '../../components/ui/index.ts'
import { errorCode, finishEmail, startEmail } from '../../lib/api.ts'
import {
  arrivedInvitation,
  dropInvitation,
  dropProviderArrival,
  signInBlocked,
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
import { TERMS_VERSION } from '../../lib/terms.ts'
import { passkeysSupported } from '../../lib/webauthn.ts'
import { AuthLayout } from './AuthLayout.tsx'
import { BlockedWords } from './Blocked.tsx'
import { CodeStep, type Sent, sentTo } from './CodeStep.tsx'
import { Field } from './Field.tsx'
import { Address } from './LinkConfirmation.tsx'
import { linkHeadlines, messageFor, needsInvitation, providerProblem } from './messages.ts'
import { PasskeySignIn } from './PasskeySignIn.tsx'
import { type ConfirmState, ProviderConfirm } from './ProviderConfirm.tsx'
import { ProviderSignIn } from './ProviderSignIn.tsx'
import { RecoveryStep } from './RecoveryStep.tsx'
import { TermsBox, termsUnticked } from './Terms.tsx'

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
 * with a reason, or to confirm an address first (ProviderConfirm.tsx). A sign-in that ends at an account the
 * operator blocked, whichever way it was proved, leads back to the first step, which says so (Blocked.tsx).
 * Signing up takes the person's agreement to the terms, a box they tick themselves, before a message is sent or
 * the browser leaves for a provider (Terms.tsx). Nothing typed here is kept beyond this page's memory.
 */
export function SignInPage({ mode }: { mode: Mode }) {
  const [started, setStarted] = useState<Started | null>(null)
  const [address, setAddress] = useState('')
  const [invitationError, setInvitationError] = useState<string | null>(null)
  const [termsError, setTermsError] = useState<string | null>(null)
  // The agreement is the person's own act: the box starts empty, and is never ticked for them. It stays ticked
  // when they come back from the code to the address, and is emptied when the terms changed meanwhile.
  const [agreed, setAgreed] = useState(false)
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
  // That an account is blocked is said on the sign-in screen, where another account can sign in.
  if (
    mode === 'sign-up' &&
    arrived?.screen === 'sign-in' &&
    arrived.kind === 'failed' &&
    arrived.reason === 'account_blocked'
  ) {
    return <Navigate to="/sign-in" replace state={location.state} />
  }

  /** The sign-in proved who it is, and the account is blocked: the first step says so, with nothing typed. */
  function blocked() {
    signInBlocked()
    setAddress('')
    setRecovering(false)
    setStarted(null)
  }

  if (recovering && mode === 'sign-in') {
    return (
      <RecoveryStep
        address={address}
        onAddress={setAddress}
        onBack={() => setRecovering(false)}
        onBlocked={blocked}
      />
    )
  }
  if (started) {
    return (
      <MailCodeStep
        mode={mode}
        started={started}
        onReplaced={setStarted}
        onBack={() => setStarted(null)}
        onBlocked={blocked}
        onInvitationRefused={(message) => {
          setInvitationError(message)
          setStarted(null)
        }}
        onTermsRefused={(message) => {
          setTermsError(message)
          setAgreed(false)
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
      termsError={termsError}
      agreed={agreed}
      onAgreed={setAgreed}
      onStarted={(next) => {
        setInvitationError(null)
        setTermsError(null)
        setStarted(next)
      }}
      onRecover={() => setRecovering(true)}
    />
  )
}

/** A sign-up (with an invitation) carries the agreement to the terms: it is only started once there is one. */
async function start(address: string, invitation: string | null): Promise<Started> {
  return {
    ...sentTo(
      address,
      await startEmail(address, invitation ?? undefined, invitation ? TERMS_VERSION : undefined),
    ),
    invitation,
  }
}

function AddressStep({
  mode,
  address,
  onAddress,
  invitationError,
  termsError,
  agreed,
  onAgreed,
  onStarted,
  onRecover,
}: {
  mode: Mode
  address: string
  onAddress: (address: string) => void
  invitationError: string | null
  /** The server refused the agreement when the sign-up ended: the terms changed meanwhile. */
  termsError: string | null
  agreed: boolean
  onAgreed: (agreed: boolean) => void
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
  const [errors, setErrors] = useState<{
    address?: string
    invitation?: string
    terms?: string
    form?: string
  }>(() => ({
    ...(invitationError ? { invitation: invitationError } : {}),
    ...(termsError ? { terms: termsError } : {}),
  }))
  const termsRef = useRef<HTMLInputElement>(null)
  const [pending, setPending] = useState(false)
  // A browser without passkeys is not offered one.
  const [passkeys] = useState(passkeysSupported)
  const addressRef = useRef<HTMLInputElement>(null)
  const invitationRef = useRef<HTMLInputElement>(null)
  // Where the caret goes once the invitation is written in groups again.
  const caret = useRef<number | null>(null)

  // The first thing to fill in takes focus: the invitation when it was refused or is what is missing, the box
  // when the agreement was refused, the address otherwise.
  const opened = useRef(false)
  // biome-ignore lint/correctness/useExhaustiveDependencies: when the step opens, and with every answer that arrives, also one that says the same as the one before
  useEffect(() => {
    if (opened.current && !reason) return
    opened.current = true
    if (invitationError || (signUp && reason)) invitationRef.current?.focus()
    else if (termsError) termsRef.current?.focus()
    else addressRef.current?.focus()
  }, [failed])

  // biome-ignore lint/correctness/useExhaustiveDependencies: runs after each change of the invitation
  useLayoutEffect(() => {
    if (caret.current === null) return
    invitationRef.current?.setSelectionRange(caret.current, caret.current)
    caret.current = null
  }, [invitation])

  /**
   * The invitation to start a provider with: none typed still starts, since the account may exist already.
   * Either way the browser leaves the sign-up screen only once the box is ticked.
   */
  function invitationTyped(): string | null | undefined {
    if (!signUp) return undefined
    const code = invitation.trim() ? normalizeInvitation(invitation) : undefined
    if (code === null) {
      dropProviderArrival('sign-in')
      setErrors({ invitation: badInvitation })
      invitationRef.current?.focus()
      return null
    }
    if (!agreed) {
      dropProviderArrival('sign-in')
      setErrors({ terms: termsUnticked })
      termsRef.current?.focus()
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
    if (signUp && !agreed) {
      setErrors({ terms: termsUnticked })
      termsRef.current?.focus()
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
      } else if (failure === 'terms_needed') {
        setErrors({ terms: messageFor(failure) })
        termsRef.current?.focus()
      } else {
        setErrors({ form: messageFor(failure) })
      }
    }
  }

  // The newest news comes first: a provider's answer, then a link that did not work.
  const problem = signUp || reason ? null : linkProblem
  // An invitation the server refused is said in its field, like one refused before the browser left.
  const refused = said && reason === 'invalid_invitation' ? said.text : undefined
  // A sign-in that ended at a blocked account, by a provider, a code, a passkey or the link of a message.
  const blocked = reason === 'account_blocked' || problem === 'blocked'
  const told = said && !refused && !blocked ? said : null
  // An account seen closing in this browser, whose day has passed: why there is no session to come back to.
  // It names nobody, since whoever reads this may not be whose account it was.
  const deleted = signUp ? null : deletedOn
  const described = [
    blocked
      ? 'account-blocked'
      : problem
        ? 'link-problem'
        : told && !signUp
          ? 'provider-problem'
          : null,
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
      {blocked && (
        <p id="account-blocked" className="mt-3 text-body text-ink-2">
          <BlockedWords /> You can still sign in to another account.
        </p>
      )}
      {problem && !blocked && (
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
        {signUp && (
          <TermsBox
            id="terms"
            checked={agreed}
            onChange={(checked) => {
              onAgreed(checked)
              setErrors(({ terms: _, ...rest }) => rest)
            }}
            error={errors.terms}
            boxRef={termsRef}
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
        terms={signUp ? TERMS_VERSION : undefined}
        onInvitationRefused={(message) => {
          setErrors({ invitation: message })
          invitationRef.current?.focus()
        }}
        onTermsRefused={(message) => {
          setErrors({ terms: message })
          termsRef.current?.focus()
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
  onBlocked,
  onInvitationRefused,
  onTermsRefused,
}: {
  mode: Mode
  started: Started
  onReplaced: (started: Started) => void
  onBack: () => void
  /** The code was right, and the account is blocked. */
  onBlocked: () => void
  onInvitationRefused: (message: string) => void
  /** The terms changed between the start and the code: the person agrees to the new ones first. */
  onTermsRefused: (message: string) => void
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
        if (failed === 'account_blocked') {
          onBlocked()
          return true
        }
        if (failed === 'terms_needed') {
          onTermsRefused(messageFor(failed))
          return true
        }
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
