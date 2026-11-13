import {
  createContext,
  type ReactNode,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
} from 'react'
import * as api from '../lib/api.ts'
import { ConfirmCancelled } from '../lib/cancelled.ts'
import { useSession } from '../lib/session.tsx'
import { cx } from '../lib/styles.ts'
import {
  getPasskey,
  PasskeyError,
  type PasskeyProblem,
  passkeysSupported,
} from '../lib/webauthn.ts'
import { CodeForm, type Sent, sentTo } from '../pages/auth/CodeStep.tsx'
import { Address } from '../pages/auth/LinkConfirmation.tsx'
import { messageFor } from '../pages/auth/messages.ts'
import { Button, Dialog, TextAction } from './ui/index.ts'

/**
 * Runs an action that may need a fresh proof of who the person is. When the server answers
 * `confirmation_needed`, the person is asked to confirm it's them, and the action runs again, once, as it was:
 * nothing they typed is asked for again. Throws `ConfirmCancelled` when they chose not to. `why` says what the
 * dialog explains: a change that decides who gets into the person's account (the default), an admin action on
 * the service, or the caller's own sentence for a change that asks for another reason.
 */
export type Confirmed = <T>(action: () => Promise<T>, why?: ConfirmWhy) => Promise<T>

/**
 * Why the dialog asks: a change that decides who gets into the person's account, an admin action, or `{ says }`,
 * the sentence of a change that asks for proof for another reason (disconnecting a mailbox deletes its mail). The
 * sentence ends saying that Permaily asks to show it's you again, and for how long that counts, as the others do.
 */
export type ConfirmWhy = 'account' | 'admin' | { says: string }

const reasons: Record<ConfirmWhy, string> = {
  account:
    "This change decides who can get into your account, so Permaily asks you to show it's you again. It counts for the next 10 minutes.",
  admin:
    "This is an admin action, so Permaily asks you to show it's you again. It counts for the next 10 minutes.",
}

const ConfirmContext = createContext<Confirmed>((action) => action())

/** The way every action that changes who gets into an account, or what an admin can do, is run. */
export function useConfirmed(): Confirmed {
  return useContext(ConfirmContext)
}

/**
 * Holds the one dialog that asks the person to confirm it's them, for every page behind the session. Two actions
 * that need it at once share one confirmation.
 */
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [asking, setAsking] = useState(false)
  // Two actions that ask at once share the dialog, which says why the first one asked.
  const [why, setWhy] = useState<ConfirmWhy>('account')
  const waiting = useRef<{ answer: Promise<boolean>; give: (confirmed: boolean) => void } | null>(
    null,
  )

  const ask = useCallback((reason: ConfirmWhy): Promise<boolean> => {
    if (waiting.current) return waiting.current.answer
    setWhy(reason)
    let give: (confirmed: boolean) => void = () => {}
    const answer = new Promise<boolean>((resolve) => {
      give = resolve
    })
    waiting.current = { answer, give }
    setAsking(true)
    return answer
  }, [])

  // The answer is given once the dialog has closed: the browser gives focus back to what opened it then, and
  // the action that runs again may open a dialog of its own, which must keep its focus.
  const answer = useRef<boolean | null>(null)
  const end = useCallback((confirmed: boolean) => {
    // The first answer counts: the dialog says it closed after it was told to.
    if (!waiting.current || answer.current !== null) return
    answer.current = confirmed
    setAsking(false)
  }, [])
  useEffect(() => {
    if (asking || answer.current === null) return
    const asked = waiting.current
    const confirmed = answer.current
    waiting.current = null
    answer.current = null
    asked?.give(confirmed)
  }, [asking])

  // The pages go, for a session that ended: whatever waited for an answer gets none, and does nothing.
  useEffect(
    () => () => {
      waiting.current?.give(false)
      waiting.current = null
    },
    [],
  )

  const confirmed = useCallback<Confirmed>(
    async (action, reason = 'account') => {
      try {
        return await action()
      } catch (error) {
        if (api.errorCode(error) !== 'confirmation_needed') throw error
        if (!(await ask(reason))) throw new ConfirmCancelled()
        return action()
      }
    },
    [ask],
  )

  return (
    <ConfirmContext.Provider value={confirmed}>
      {children}
      <ConfirmDialog open={asking} why={why} onDone={end} />
    </ConfirmContext.Provider>
  )
}

function ConfirmDialog({
  open,
  why,
  onDone,
}: {
  open: boolean
  why: ConfirmWhy
  onDone: (confirmed: boolean) => void
}) {
  const [busy, setBusy] = useState(false)
  return (
    <Dialog
      open={open}
      dismissable={!busy}
      onClose={() => onDone(false)}
      labelledBy="confirm-heading"
    >
      {open && <ConfirmSteps why={why} onDone={onDone} onBusy={setBusy} />}
    </Dialog>
  )
}

type Step =
  | { kind: 'loading' }
  | { kind: 'failed'; text: string }
  | { kind: 'choose' }
  | { kind: 'code'; sent: Sent }

/** What the dialog says after a try that confirmed nothing. A prompt the person closed is not an error. */
type Outcome = { quiet: boolean; text: string }

const passkeyProblems: Record<PasskeyProblem, Outcome> = {
  cancelled: { quiet: true, text: 'No passkey was used. You can try again.' },
  exists: { quiet: false, text: "The passkey couldn't be read. Try again." },
  unavailable: { quiet: false, text: "This browser can't use a passkey here." },
  failed: { quiet: false, text: "The passkey couldn't be read. Try again." },
}

/** Said when the server will not send a code to this account: it confirms with a passkey. */
const refusedCode = 'This account confirms with a passkey, not with a code.'

/** How long options asked for ahead of the click are used: the server keeps a ceremony a few minutes. */
const freshFor = 120_000

type Prepared = api.PasskeyCeremony<PublicKeyCredentialRequestOptionsJSON> & { at: number }

function ConfirmSteps({
  why,
  onDone,
  onBusy,
}: {
  why: ConfirmWhy
  onDone: (confirmed: boolean) => void
  onBusy: (busy: boolean) => void
}) {
  const session = useSession()
  const { failed, confirmed } = session
  const address = session.state.status === 'signedIn' ? session.state.me.account.address : ''
  const [supported] = useState(passkeysSupported)
  const [step, setStep] = useState<Step>({ kind: 'loading' })
  const [ways, setWays] = useState<api.ConfirmWay[]>([])
  const [outcome, setOutcome] = useState<Outcome | null>(null)
  // What is under way: the browser's prompt, the message being sent, or the code being checked.
  const [pending, setPending] = useState<'passkey' | 'email' | 'code' | null>(null)
  const headingRef = useRef<HTMLHeadingElement>(null)
  const outcomeRef = useRef<HTMLParagraphElement>(null)
  const leaving = useRef<AbortController | null>(null)
  // The options of a passkey ceremony, asked for before the person clicks, so the browser's prompt opens
  // straight from their click: a browser such as Safari refuses a prompt opened long after it.
  const prepared = useRef<Prepared | null>(null)
  const passkey = supported && ways.includes('passkey')
  const email = ways.includes('email')

  useEffect(() => {
    onBusy(pending !== null)
  }, [pending, onBusy])

  const prepare = useCallback(() => {
    prepared.current = null
    api.startConfirmPasskey().then(
      (ceremony) => {
        prepared.current = { ...ceremony, at: Date.now() }
      },
      // Said when the person asks for a passkey, if it fails again then.
      () => {},
    )
  }, [])

  const load = useCallback(async () => {
    setStep({ kind: 'loading' })
    setOutcome(null)
    try {
      const answer = await api.confirmWays()
      // Confirmed meanwhile, such as in another tab: there is nothing to ask.
      if (answer.confirmed_until && Date.parse(answer.confirmed_until) > Date.now()) {
        onDone(true)
        return
      }
      setWays(answer.ways)
      setStep({ kind: 'choose' })
      if (supported && answer.ways.includes('passkey')) prepare()
    } catch (error) {
      const code = failed(error)
      if (code) setStep({ kind: 'failed', text: `This couldn't be checked. ${messageFor(code)}` })
    }
  }, [failed, onDone, prepare, supported])

  useEffect(() => {
    void load()
    // Leaving the dialog closes a prompt that is still open.
    const controller = new AbortController()
    leaving.current = controller
    return () => controller.abort()
  }, [load])

  // What went wrong takes focus when the action that was used is gone with it.
  const lost = useRef(false)
  useEffect(() => {
    if (!outcome || !lost.current) return
    lost.current = false
    outcomeRef.current?.focus()
  }, [outcome])

  /** The account no longer confirms this way: its action goes, and what is said of it takes focus. */
  function drop(way: api.ConfirmWay, text: string) {
    lost.current = true
    setWays((now) => now.filter((one) => one !== way))
    setOutcome({ quiet: false, text })
  }

  function back() {
    setOutcome(null)
    setStep({ kind: 'choose' })
    headingRef.current?.focus()
  }

  function done(me: api.Me) {
    confirmed(me)
    onDone(true)
  }

  function confirmWithPasskey() {
    if (pending) return
    const ready =
      prepared.current && Date.now() - prepared.current.at < freshFor ? prepared.current : null
    prepared.current = null
    setOutcome(null)
    setPending('passkey')
    void withPasskey(ready)
  }

  async function withPasskey(ready: Prepared | null) {
    const signal = leaving.current?.signal
    try {
      // With options at hand the prompt opens now, in the click. Without them, they are asked for first.
      const { ceremony, options } = ready ?? (await api.startConfirmPasskey())
      const credential = await getPasskey(options, signal)
      done(await api.finishConfirmPasskey(ceremony, credential))
    } catch (failure) {
      if (signal?.aborted) return
      setPending(null)
      if (failure instanceof PasskeyError) {
        const said = passkeyProblems[failure.kind]
        setOutcome(
          failure.kind === 'unavailable' && email
            ? { ...said, text: `${said.text} Get a code by email instead.` }
            : said,
        )
        prepare()
        return
      }
      const code = failed(failure)
      if (!code) return
      if (code === 'passkey_needed') {
        drop('passkey', 'This account has no passkey any more.')
        return
      }
      setOutcome({
        quiet: false,
        text:
          code === 'invalid_passkey'
            ? "That passkey didn't confirm it's you. Use one of this account's passkeys, then try again."
            : messageFor(code),
      })
      prepare()
    }
  }

  async function sendCode() {
    if (pending) return
    setOutcome(null)
    setPending('email')
    try {
      setStep({ kind: 'code', sent: sentTo(address, await api.startConfirmEmail()) })
    } catch (failure) {
      const code = failed(failure)
      if (!code) return
      if (code === 'passkey_required') drop('email', refusedCode)
      else setOutcome({ quiet: false, text: `No message was sent. ${messageFor(code)}` })
    } finally {
      setPending(null)
    }
  }

  const heading = (
    <h2
      id="confirm-heading"
      ref={headingRef}
      tabIndex={-1}
      data-dialog-focus
      className="text-heading font-medium outline-none"
    >
      Confirm it's you
    </h2>
  )

  if (step.kind === 'code') {
    return (
      <>
        {heading}
        <CodeForm
          id="confirm-code"
          sent={step.sent}
          intro={
            <>
              A message with a code is on its way to <Address>{address}</Address>.
            </>
          }
          purpose="Enter the code to confirm it's you."
          submitLabel="Confirm"
          finish={async (code) => {
            setPending('code')
            try {
              done(await api.finishConfirmEmail(step.sent.attempt, code))
            } finally {
              setPending(null)
            }
          }}
          ended={(code) => {
            if (code === 'passkey_required') {
              setStep({ kind: 'choose' })
              drop('email', refusedCode)
              return true
            }
            // The session ended, or the account is being deleted: the screen that says so follows.
            return failed(new api.ApiError(code)) === null
          }}
          sendAnother={async () => {
            setStep({ kind: 'code', sent: sentTo(address, await api.startConfirmEmail()) })
          }}
          onBack={back}
          backLabel="Back"
        />
      </>
    )
  }

  const failedOutcome = outcome !== null && !outcome.quiet
  return (
    <>
      {heading}
      <p className="mt-3 text-body text-ink-2">
        {typeof why === 'string' ? reasons[why] : why.says}
      </p>
      {step.kind === 'loading' && (
        <p role="status" className="mt-6 text-small text-ink-2">
          Loading
        </p>
      )}
      {step.kind === 'failed' && (
        <div role="alert" className="mt-6 flex flex-wrap items-baseline gap-x-3 gap-y-1">
          <p className="text-small text-danger">{step.text}</p>
          <TextAction onClick={() => void load()}>Try again</TextAction>
        </div>
      )}
      {step.kind === 'choose' && (
        <div className="mt-6 flex flex-col items-start gap-4">
          {passkey && (
            <Button onClick={confirmWithPasskey} aria-disabled={pending !== null}>
              Use a passkey
            </Button>
          )}
          {email && (
            <div className="flex flex-col items-start gap-1.5">
              <Button
                variant={passkey ? 'secondary' : 'primary'}
                onClick={sendCode}
                aria-disabled={pending !== null}
              >
                {passkey ? 'Get a code by email instead' : 'Email me a code'}
              </Button>
              <p className="text-small text-ink-2">
                It goes to <Address>{address}</Address>.
              </p>
            </div>
          )}
          {!passkey && !email && (
            <p className="text-body text-ink-2">
              There's no way to confirm it's you in this browser. Try again in a browser that can
              use your passkeys.
            </p>
          )}
        </div>
      )}
      <p
        ref={outcomeRef}
        tabIndex={-1}
        role={failedOutcome ? 'alert' : 'status'}
        className={cx(
          'mt-4 text-small outline-none empty:hidden',
          failedOutcome ? 'text-danger' : 'text-ink-2',
        )}
      >
        {pending === 'passkey'
          ? 'Waiting for your browser'
          : pending === 'email'
            ? 'Sending the message'
            : outcome?.text}
      </p>
      <div className="mt-8 flex flex-wrap gap-x-5 gap-y-2 border-t border-rule pt-4">
        <TextAction
          onClick={() => pending === null && onDone(false)}
          aria-disabled={pending !== null}
        >
          Cancel
        </TextAction>
      </div>
    </>
  )
}
