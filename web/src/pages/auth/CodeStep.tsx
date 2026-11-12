import { type FormEvent, type ReactNode, useEffect, useRef, useState } from 'react'
import { Button, Headline, TextAction } from '../../components/ui/index.ts'
import { type ApiErrorCode, type EmailStarted, errorCode } from '../../lib/api.ts'
import { AuthLayout } from './AuthLayout.tsx'
import { Field } from './Field.tsx'
import { messageFor } from './messages.ts'

/** A message the server was asked to send: its code is entered against `attempt`. */
export type Sent = {
  attempt: string
  address: string
  /** When the code and the link stop working, in the browser's clock. */
  expiresAt: number
  minutes: number
}

export function sentTo(address: string, answer: EmailStarted): Sent {
  return {
    attempt: answer.attempt,
    address,
    expiresAt: Date.now() + answer.expires_in * 1000,
    minutes: Math.max(1, Math.round(answer.expires_in / 60)),
  }
}

const codeDigits = 6

type CodeFormProps = {
  sent: Sent
  /** What was sent and where, before how long it works for. */
  intro: ReactNode
  /** What the code ends, when that is more than signing in by mail. */
  purpose?: string
  /** Under the field: what to do with a message that has no code. */
  hint?: string
  /** Under the form: what else the person may need to know. */
  footnote?: ReactNode
  submitLabel: string
  /** Ends it with the code. Throws an ApiError when the server refused or gave no answer. */
  finish: (code: string) => Promise<void>
  /** Takes a refusal that ends this step, and says whether it did. A wrong code and no network stay here. */
  ended?: (failure: ApiErrorCode) => boolean
  /** Asks for another message. Whoever uses the step gives it the new `sent`. */
  sendAnother: () => Promise<void>
  /** Its own words for why another message was not sent, when the usual ones do not fit. */
  notSent?: (failure: ApiErrorCode) => string | undefined
  onBack: () => void
}

/**
 * The step that asks for the six-digit code of a message, as a screen of its own: for signing in or up by
 * mail, and for confirming an address after a provider that did not vouch for one.
 */
export function CodeStep(props: CodeFormProps) {
  return (
    <AuthLayout title="Check your mail">
      <Headline size="display-s">Check your mail</Headline>
      <CodeForm {...props} />
    </AuthLayout>
  )
}

/**
 * What asks for the six-digit code of a message, under whatever heading it is put: the sign-in screens
 * (`CodeStep`) and the dialog that changes the contact address in Settings. What the code ends, and what is
 * said of it, come from whoever uses it. A wrong code, too many tries and a code whose time has passed read
 * the same everywhere. The code lives only in this form's memory.
 */
export function CodeForm({
  sent,
  intro,
  purpose,
  hint,
  footnote,
  submitLabel,
  finish,
  ended,
  sendAnother,
  notSent,
  onBack,
}: CodeFormProps) {
  const [code, setCode] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [formError, setFormError] = useState<string | null>(null)
  const [note, setNote] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const [expired, setExpired] = useState(() => sent.expiresAt <= Date.now())
  const codeRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    codeRef.current?.focus()
  }, [])

  // The message says how long it works for. When that passes, the page says so instead of failing later.
  useEffect(() => {
    const left = sent.expiresAt - Date.now()
    setExpired(left <= 0)
    if (left <= 0) return
    const timer = window.setTimeout(() => setExpired(true), left)
    return () => window.clearTimeout(timer)
  }, [sent.expiresAt])

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
      await finish(code)
    } catch (failure) {
      const failed = errorCode(failure)
      if (ended?.(failed)) return
      setPending(false)
      if (failed === 'invalid_code') {
        setError(messageFor(failed))
        codeRef.current?.focus()
        codeRef.current?.select()
      } else {
        setFormError(messageFor(failed))
      }
    }
  }

  async function another() {
    if (pending) return
    setError(null)
    setFormError(null)
    setNote(null)
    setPending(true)
    try {
      await sendAnother()
      setCode('')
      setNote('Asked for another message. Use the code or the link of the newest one.')
    } catch (failure) {
      const failed = errorCode(failure)
      const own = notSent?.(failed)
      if (!own && ended?.(failed)) return
      setFormError(own ?? messageFor(failed))
    }
    setPending(false)
    codeRef.current?.focus()
  }

  return (
    <>
      <p id="code-intro" className="mt-3 text-body text-ink-2">
        {intro}{' '}
        {expired
          ? 'It has expired by now.'
          : `It works for ${sent.minutes} ${sent.minutes === 1 ? 'minute' : 'minutes'}.`}
        {purpose && ` ${purpose}`}
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
          hint={hint}
          error={error}
          describedBy="code-intro"
        />
        <p role="alert" className="text-small text-danger empty:hidden">
          {formError}
        </p>
        <Button type="submit" disabled={pending}>
          {submitLabel}
        </Button>
      </form>

      <p role="status" className="mt-4 text-small text-ink-2 empty:hidden">
        {note}
      </p>
      {footnote && <p className="mt-4 text-small text-ink-2">{footnote}</p>}
      <div className="mt-8 flex flex-wrap gap-x-5 gap-y-2 border-t border-rule pt-4">
        <TextAction onClick={another} disabled={pending}>
          Send another message
        </TextAction>
        <TextAction onClick={onBack} disabled={pending}>
          Use another address
        </TextAction>
      </div>
    </>
  )
}
