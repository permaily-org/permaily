import { type FormEvent, useRef, useState } from 'react'
import { useNavigate } from 'react-router'
import { Button, Headline } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'
import { Field } from '../auth/Field.tsx'
import { messageFor } from '../auth/messages.ts'
import { maxLabel } from './AccountsPage.tsx'
import { FlowFrame } from './MailboxLayout.tsx'

/**
 * Connecting a mailbox: a label, then the server makes the mailbox and its address, and the forwarding set-up
 * opens. Connecting with a Google account comes later: it is said, and offered as nothing.
 */
export function ConnectMailboxPage() {
  const { failed } = useSession()
  const navigate = useNavigate()
  const [label, setLabel] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const fieldRef = useRef<HTMLInputElement>(null)

  async function make(event: FormEvent) {
    event.preventDefault()
    if (pending) return
    const typed = label.trim()
    if ([...typed].length > maxLabel || /[\p{Cc}\p{Zl}\p{Zp}]/u.test(typed)) {
      setError(messageFor('invalid_label'))
      fieldRef.current?.focus()
      return
    }
    setError(null)
    setPending(true)
    try {
      const made = await api.createMailbox(typed)
      navigate(`/accounts/${encodeURIComponent(made.id)}/forwarding`)
    } catch (failure) {
      setPending(false)
      const code = failed(failure)
      if (code === 'invalid_label') {
        setError(messageFor(code))
        fieldRef.current?.focus()
      } else if (code) {
        setError(`No mailbox was made. ${messageFor(code)}`)
      }
    }
  }

  return (
    <FlowFrame>
      <div className="max-w-xl">
        <Headline className="mt-10">Connect a mailbox</Headline>
        <p className="mt-3 text-lead text-ink-2">
          Permaily makes an address of your own. You forward your Gmail to it, and read the mail
          here.
        </p>
        <form noValidate onSubmit={make} className="mt-8 flex flex-col gap-5">
          <Field
            id="mailbox-label"
            label="Label (optional)"
            hint="Only you see it, such as Personal or Work."
            autoComplete="off"
            inputRef={fieldRef}
            value={label}
            onChange={(event) => setLabel(event.target.value)}
            error={error}
          />
          <div>
            <Button type="submit" aria-disabled={pending}>
              Make the address
            </Button>
          </div>
        </form>
        <p className="mt-10 border-t border-rule pt-4 text-small text-ink-2">
          Connecting with a Google account is coming later.
        </p>
      </div>
    </FlowFrame>
  )
}
