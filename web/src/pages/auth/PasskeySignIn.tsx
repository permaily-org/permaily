import { KeyRound } from 'lucide-react'
import { useEffect, useRef, useState } from 'react'
import { Button } from '../../components/ui/index.ts'
import { errorCode, finishPasskeySignIn, startPasskeySignIn } from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'
import { cx } from '../../lib/styles.ts'
import { getPasskey, PasskeyError, type PasskeyProblem } from '../../lib/webauthn.ts'
import { messageFor } from './messages.ts'

/** What the screen says after a try that signed nobody in. A prompt the person closed is not an error. */
type Outcome = { quiet: boolean; text: string }

const problems: Record<PasskeyProblem, Outcome> = {
  cancelled: { quiet: true, text: 'No passkey was used. You can try again.' },
  exists: { quiet: false, text: "The passkey couldn't be read. Try again." },
  unavailable: {
    quiet: false,
    text: "This browser can't use a passkey here. Sign in with your email address instead.",
  },
  failed: { quiet: false, text: "The passkey couldn't be read. Try again." },
}

/**
 * Signing in with a passkey: one action and no address, because the browser shows the passkeys it has and the
 * passkey says which account it is for. The prompt opens only when the person asks for it, never by itself.
 */
export function PasskeySignIn() {
  const session = useSession()
  const [pending, setPending] = useState(false)
  const [outcome, setOutcome] = useState<Outcome | null>(null)
  const buttonRef = useRef<HTMLButtonElement>(null)
  const leaving = useRef<AbortController | null>(null)

  // Leaving the screen closes a prompt that is still open.
  useEffect(() => {
    const controller = new AbortController()
    leaving.current = controller
    return () => controller.abort()
  }, [])

  // After a try that did not sign in, focus is back on the action, once it can be used again.
  useEffect(() => {
    if (outcome) buttonRef.current?.focus()
  }, [outcome])

  async function signIn() {
    if (pending) return
    setOutcome(null)
    setPending(true)
    const signal = leaving.current?.signal
    try {
      const { ceremony, options } = await startPasskeySignIn()
      const credential = await getPasskey(options, signal)
      session.signedIn(await finishPasskeySignIn(ceremony, credential))
    } catch (failure) {
      if (signal?.aborted) return
      setPending(false)
      if (failure instanceof PasskeyError) {
        setOutcome(problems[failure.kind])
        return
      }
      const code = errorCode(failure)
      setOutcome({
        quiet: false,
        text:
          code === 'invalid_passkey'
            ? "That passkey didn't sign you in. It may have been removed from its account, or the wait was too long. Try again, or sign in another way."
            : messageFor(code),
      })
    }
  }

  return (
    <>
      <Button ref={buttonRef} variant="secondary" disabled={pending} onClick={signIn}>
        <KeyRound aria-hidden size={16} />
        Sign in with a passkey
      </Button>
      {/* One place for both: a closed prompt is said calmly, a refusal as an error. */}
      <p
        role={outcome && !outcome.quiet ? 'alert' : 'status'}
        className={cx(
          'text-small empty:hidden',
          outcome && !outcome.quiet ? 'text-danger' : 'text-ink-2',
        )}
      >
        {outcome?.text}
      </p>
    </>
  )
}
