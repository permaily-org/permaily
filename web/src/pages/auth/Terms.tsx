import { type RefObject, useEffect, useRef, useState } from 'react'
import { Button, Checkbox, TextAnchor } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'
import { privacyUrl, termsUrl } from '../../lib/terms.ts'
import { DeleteAccount } from '../settings/account/DeleteAccount.tsx'
import { AuthLayout } from './AuthLayout.tsx'
import { FocusedHeadline } from './LinkConfirmation.tsx'
import { messageFor } from './messages.ts'

/** Said when the box was not ticked: nothing was sent. */
export const termsUnticked = 'Tick the box to agree to the terms of service and the privacy policy.'

/**
 * The person's agreement to the terms of service and the privacy policy: a box they tick themselves, never
 * ticked for them, next to a sentence that links both. The documents open in a new tab, so nothing typed here
 * is lost by reading them.
 */
export function TermsBox({
  id,
  checked,
  onChange,
  error,
  boxRef,
}: {
  id: string
  checked: boolean
  onChange: (checked: boolean) => void
  error?: string | null
  boxRef?: RefObject<HTMLInputElement | null>
}) {
  const errorId = `${id}-error`
  return (
    <div className="flex flex-col gap-1.5">
      <Checkbox
        id={id}
        ref={boxRef}
        top
        label={
          <span className="text-small text-ink-2">
            I agree to Permaily's{' '}
            <TextAnchor href={termsUrl} target="_blank" rel="noopener noreferrer">
              terms of service
            </TextAnchor>{' '}
            and{' '}
            <TextAnchor href={privacyUrl} target="_blank" rel="noopener noreferrer">
              privacy policy
            </TextAnchor>
            .
          </span>
        }
        checked={checked}
        aria-invalid={error ? true : undefined}
        aria-describedby={errorId}
        onChange={(event) => onChange(event.target.checked)}
      />
      {/* Always in the page, so a screen reader hears the error the moment it is written. */}
      <p id={errorId} role="alert" className="text-small text-danger empty:hidden">
        {error}
      </p>
    </div>
  )
}

const title = 'The terms changed'

/**
 * All that an account shows once the terms in force are not the ones its person agreed to, or they agreed to
 * none: the links, the box and one action, until they agree. They can also sign out, or delete the account,
 * which the server lets through without the agreement.
 */
export function TermsChanged({
  account,
  version,
}: {
  account: api.Account
  /** The version in force, as the server said when this screen was drawn: the one the person agrees to. */
  version: string
}) {
  const { accountChanged, failed, signOut } = useSession()
  const [agreed, setAgreed] = useState(false)
  const [pending, setPending] = useState<'agree' | 'leave' | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [unticked, setUnticked] = useState<string | null>(null)
  const headingRef = useRef<HTMLHeadingElement>(null)
  const boxRef = useRef<HTMLInputElement>(null)
  const agreeRef = useRef<HTMLButtonElement>(null)
  const leaveRef = useRef<HTMLButtonElement>(null)
  const tried = useRef(agreeRef)

  // The screen appears by itself, so its headline takes focus: a screen reader starts there.
  useEffect(() => {
    headingRef.current?.focus()
  }, [])

  useEffect(() => {
    if (error) tried.current.current?.focus()
  }, [error])

  async function agree() {
    if (pending) return
    setError(null)
    tried.current = agreeRef
    if (!agreed) {
      setUnticked(termsUnticked)
      boxRef.current?.focus()
      return
    }
    setPending('agree')
    try {
      accountChanged(await api.agreeTerms(version))
    } catch (failure) {
      const code = failed(failure)
      setPending(null)
      if (code) {
        setError(
          code === 'network'
            ? "Your agreement wasn't saved: Permaily can't be reached. Check your connection, then try again."
            : `Your agreement wasn't saved. ${messageFor(code)}`,
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
      <FocusedHeadline headingRef={headingRef} describedBy="terms-changed-why">
        {title}
      </FocusedHeadline>
      <div className="mt-3 flex flex-col gap-3 text-body text-ink-2">
        <p id="terms-changed-why">
          {account.terms
            ? "Permaily's terms of service and privacy policy changed since you agreed to them."
            : 'Permaily now has terms of service and a privacy policy.'}{' '}
          Read them, and agree to keep using your account.
        </p>
      </div>
      <div className="mt-6">
        <TermsBox
          id="terms-agree"
          checked={agreed}
          onChange={(checked) => {
            setAgreed(checked)
            setUnticked(null)
          }}
          error={unticked}
          boxRef={boxRef}
        />
      </div>
      <p role="alert" className="mt-5 text-small text-danger empty:hidden">
        {error}
      </p>
      <p role="status" className="sr-only">
        {pending === 'agree' ? 'Saving your agreement' : pending === 'leave' ? 'Signing out' : ''}
      </p>
      <div className="mt-6 flex flex-wrap gap-3">
        <Button ref={agreeRef} className="flex-1" aria-disabled={pending !== null} onClick={agree}>
          Agree and continue
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
      <p className="mt-8 text-small text-ink-2">
        If you don't agree, you can sign out, or delete your account.
      </p>
      <DeleteAccount />
    </AuthLayout>
  )
}
