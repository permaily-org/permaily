import { type FormEvent, useEffect, useRef, useState } from 'react'
import { Button, Dialog, TextAction } from '../../../components/ui/index.ts'
import * as api from '../../../lib/api.ts'
import { useSession } from '../../../lib/session.tsx'
import { CodeForm, type Sent, sentTo } from '../../auth/CodeStep.tsx'
import { Field } from '../../auth/Field.tsx'
import { Address } from '../../auth/LinkConfirmation.tsx'
import { messageFor } from '../../auth/messages.ts'
import { SettingRow } from '../rows.tsx'

/** The server would not send a code there. It says the same of an address it cannot use and of the account's own. */
const refused =
  "Permaily can't use that address. Check it for typos, and that it isn't the one this account has already."

/** Said only after the code: whoever reads it has shown that the address is theirs. */
const inUse =
  "That address belongs to another Permaily account, so it can't be this one's contact email. Use another address."

const changed =
  'Contact email changed. Permaily sent a notice to the old address and to the new one.'

/**
 * The account's contact address, and the way to change it: the new address, then the six-digit code of the
 * message the server sends there, which is the code step signing in has (`CodeForm`). Nothing changes until
 * the code is right. Then the server's answer goes to the session, so everything that shows the address
 * follows, and the row says that both addresses were told. The new address, the code and the server's name
 * for the change live only in this component's memory: never in storage or in the address bar.
 */
export function ContactAddress({ account }: { account: api.Account }) {
  const { accountChanged, failed } = useSession()
  const [open, setOpen] = useState(false)
  const [typed, setTyped] = useState('')
  const [sent, setSent] = useState<Sent | null>(null)
  const [errors, setErrors] = useState<{ address?: string; form?: string }>({})
  const [pending, setPending] = useState(false)
  const [said, setSaid] = useState('')
  const addressRef = useRef<HTMLInputElement>(null)

  // The field takes focus when the dialog opens, and when the person comes back to it from the code.
  const asking = open && sent === null
  useEffect(() => {
    if (asking) addressRef.current?.focus()
  }, [asking])

  function start() {
    setTyped('')
    setSent(null)
    setErrors({})
    setSaid('')
    setOpen(true)
  }

  async function send(event: FormEvent) {
    event.preventDefault()
    if (pending) return
    const address = typed.trim()
    if (!address.includes('@')) {
      setErrors({ address: 'Enter the new email address.' })
      addressRef.current?.focus()
      return
    }
    if (address.toLowerCase() === account.address.toLowerCase()) {
      setErrors({ address: 'That is your contact email already.' })
      addressRef.current?.focus()
      return
    }
    setErrors({})
    setPending(true)
    try {
      setSent(sentTo(address, await api.startAddressChange(address)))
    } catch (failure) {
      // Nothing is said here when the session ended or the account is being deleted: its own screen follows.
      const code = failed(failure)
      if (code === 'invalid_address') setErrors({ address: refused })
      else if (code) {
        setErrors({
          form:
            code === 'network'
              ? "No message was sent: Permaily can't be reached. Check your connection, then try again."
              : `No message was sent. ${messageFor(code)}`,
        })
      }
      addressRef.current?.focus()
    } finally {
      setPending(false)
    }
  }

  return (
    <>
      <SettingRow
        label="Contact email"
        action={
          <TextAction onClick={start}>
            Change<span className="sr-only"> contact email</span>
          </TextAction>
        }
      >
        <span className="block wrap-anywhere font-mono text-small">{account.address}</span>
        <span role="status" className="block text-small text-ink-2 empty:hidden">
          {said}
        </span>
      </SettingRow>

      <Dialog
        open={open}
        dismissable={!pending}
        onClose={() => setOpen(false)}
        labelledBy="change-address"
      >
        {open && (
          <>
            <h2 id="change-address" className="text-heading font-medium">
              {sent ? 'Check your mail' : 'Change your contact email'}
            </h2>
            {sent ? (
              <CodeForm
                sent={sent}
                intro={
                  <>
                    A message with a code is on its way to <Address>{sent.address}</Address>.
                  </>
                }
                purpose="Enter the code to make that address your contact email."
                submitLabel="Confirm new address"
                finish={async (code) => {
                  setPending(true)
                  try {
                    const next = await api.finishAddressChange(sent.attempt, code)
                    setOpen(false)
                    setSaid(changed)
                    // Everything that shows the address follows: the session has the server's answer.
                    accountChanged(next)
                  } finally {
                    setPending(false)
                  }
                }}
                ended={(code) => {
                  if (code === 'address_in_use') {
                    setErrors({ address: inUse })
                    setSent(null)
                    return true
                  }
                  // The session ended, or the account is being deleted: the session hears of it, and the
                  // screen that says so follows. Anything else is said by the code step.
                  return failed(new api.ApiError(code)) === null
                }}
                sendAnother={async () => {
                  setSent(sentTo(sent.address, await api.startAddressChange(sent.address)))
                }}
                onBack={() => {
                  setErrors({})
                  setSent(null)
                }}
              />
            ) : (
              <>
                <div className="mt-3 flex flex-col gap-3 text-body text-ink-2">
                  <p>
                    Your contact email is <Address>{account.address}</Address>. Permaily writes to
                    you there, and it is the address you sign in with.
                  </p>
                  <p id="change-address-how">
                    Permaily sends a message with a code to the new address, to check that it is
                    yours. Your contact email changes only when you enter that code.
                  </p>
                </div>
                <form noValidate onSubmit={send} className="mt-8 flex flex-col gap-5">
                  <Field
                    id="new-address"
                    label="New email address"
                    type="email"
                    name="email"
                    autoComplete="email"
                    inputMode="email"
                    autoCapitalize="none"
                    autoCorrect="off"
                    spellCheck={false}
                    required
                    inputRef={addressRef}
                    value={typed}
                    onChange={(event) => setTyped(event.target.value)}
                    error={errors.address}
                    describedBy="change-address-how"
                  />
                  <p role="alert" className="text-small text-danger empty:hidden">
                    {errors.form}
                  </p>
                  <Button type="submit" disabled={pending}>
                    Send the code
                  </Button>
                </form>
                <div className="mt-8 flex flex-wrap gap-x-5 gap-y-2 border-t border-rule pt-4">
                  <TextAction onClick={() => setOpen(false)} disabled={pending}>
                    Keep this address
                  </TextAction>
                </div>
              </>
            )}
          </>
        )}
      </Dialog>
    </>
  )
}
