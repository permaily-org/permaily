import { useRef, useState } from 'react'
import { useConfirmed } from '../../../components/Confirm.tsx'
import { Button, Dialog } from '../../../components/ui/index.ts'
import * as api from '../../../lib/api.ts'
import { isCancelled } from '../../../lib/cancelled.ts'
import { closingDay, closingDays, formatFullDay } from '../../../lib/closing.ts'
import { useSession } from '../../../lib/session.tsx'
import { cx } from '../../../lib/styles.ts'
import { solidDanger } from '../../accounts/styles.ts'
import { Field } from '../../auth/Field.tsx'
import { Address } from '../../auth/LinkConfirmation.tsx'
import { messageFor } from '../../auth/messages.ts'

/**
 * Deleting the account. The dialog says what happens before the server is asked, and the person types the
 * account's address to go on: nothing here is the default, so Enter deletes nothing, not on the question and
 * not in the field.
 */
export function DeleteAccount() {
  const { state, accountChanged, failed } = useSession()
  const confirmed = useConfirmed()
  // The day the account would be removed, read from the clock when the question opens.
  const [asked, setAsked] = useState<Date | null>(null)
  const [typed, setTyped] = useState('')
  const [pending, setPending] = useState(false)
  const [mismatch, setMismatch] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const inputRef = useRef<HTMLInputElement>(null)

  if (state.status !== 'signedIn') return null
  const { address } = state.me.account
  const matches = typed.trim().toLowerCase() === address.toLowerCase()

  function open() {
    setTyped('')
    setMismatch(null)
    setError(null)
    setAsked(closingDay(Date.now()))
  }

  /** Says what is missing when the address was not typed. True when it was. */
  function check(): boolean {
    if (matches) {
      setMismatch(null)
      return true
    }
    setMismatch(
      typed.trim()
        ? "That isn't this account's address. Type it as it is written above."
        : 'Type the address of this account to delete it.',
    )
    inputRef.current?.focus()
    return false
  }

  async function remove() {
    if (pending || !check()) return
    setError(null)
    setPending(true)
    try {
      // The account is closing from this answer on: the app shows that and nothing else.
      accountChanged(await confirmed(() => api.deleteAccount()))
    } catch (failure) {
      // The person chose not to confirm it's them: the question stays, as it was.
      if (isCancelled(failure)) {
        setPending(false)
        return
      }
      const code = failed(failure)
      setPending(false)
      if (code) {
        setError(
          code === 'network'
            ? "Permaily can't be reached, so it isn't known whether your account is being deleted. Check your connection, then try again."
            : `Your account isn't being deleted. ${messageFor(code)}`,
        )
      }
    }
  }

  return (
    <>
      <section
        aria-labelledby="delete-account"
        className="mt-8 flex flex-col items-start gap-3 border border-danger px-4 py-4 sm:flex-row sm:items-center sm:gap-6 sm:px-5"
      >
        <h2 id="delete-account" className="min-w-0 flex-1 font-medium">
          Delete your account
        </h2>
        <Button variant="danger" onClick={open}>
          Delete my account
        </Button>
      </section>

      <Dialog
        open={asked !== null}
        dismissable={!pending}
        onClose={() => setAsked(null)}
        labelledBy="delete-account-dialog"
      >
        {asked && (
          // Enter in the field deletes nothing: it only says whether the address is the one.
          <form
            noValidate
            onSubmit={(event) => {
              event.preventDefault()
              check()
            }}
          >
            <h2
              id="delete-account-dialog"
              tabIndex={-1}
              data-dialog-focus
              className="text-heading font-medium outline-none"
            >
              Delete your account?
            </h2>
            <div className="mt-3 flex flex-col gap-3 text-body text-ink-2">
              <p>
                Your account stops working as soon as you confirm, and every other device is signed
                out.
              </p>
              <p>
                It is deleted for good in {closingDays} days, on{' '}
                <time dateTime={asked.toISOString()} className="font-medium text-ink">
                  {formatFullDay(asked)}
                </time>
                . Until then you can sign in and cancel.
              </p>
              <p>
                After that day nothing can bring it back. Your address can only return with a new
                invitation, as a new account.
              </p>
              <p id="delete-account-address">
                To go on, type the address of this account: <Address>{address}</Address>
              </p>
            </div>
            <div className="mt-5">
              <Field
                id="delete-account-typed"
                label="Address of this account"
                inputRef={inputRef}
                type="text"
                inputMode="email"
                autoComplete="off"
                autoCapitalize="none"
                autoCorrect="off"
                spellCheck={false}
                value={typed}
                onChange={(event) => {
                  setTyped(event.target.value)
                  setMismatch(null)
                }}
                error={mismatch}
                describedBy="delete-account-address"
                className="font-mono"
              />
            </div>
            <p role="alert" className="mt-4 text-small text-danger empty:hidden">
              {error}
            </p>
            <p role="status" className="sr-only">
              {pending ? 'Asking to delete your account' : ''}
            </p>
            <div className="mt-6 flex flex-wrap gap-3">
              <Button variant="secondary" disabled={pending} onClick={() => setAsked(null)}>
                Keep my account
              </Button>
              <button
                type="button"
                className={cx(solidDanger, pending && 'opacity-60')}
                aria-disabled={pending}
                onClick={remove}
              >
                Delete my account
              </button>
            </div>
          </form>
        )}
      </Dialog>
    </>
  )
}
