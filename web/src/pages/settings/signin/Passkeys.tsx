import { type FormEvent, type RefObject, useEffect, useRef, useState } from 'react'
import { Button, Dialog, TextAction } from '../../../components/ui/index.ts'
import * as api from '../../../lib/api.ts'
import { describeDevice } from '../../../lib/device.ts'
import { actionClass, cx } from '../../../lib/styles.ts'
import { makePasskey, PasskeyError, type PasskeyProblem } from '../../../lib/webauthn.ts'
import { solidDanger } from '../../accounts/styles.ts'
import { messageFor } from '../../auth/messages.ts'
import { field, SettingRow } from '../rows.tsx'
import { formatDay, maxName, passkeyName, type SignInSection } from './shared.ts'

const badName = 'Give it a name of 1 to 64 characters, on one line.'

/** The account's passkeys, each with its name, when it was made and whether it is synced, and the form to add one. */
export function Passkeys({
  list,
  supported,
  adding,
  onAddingDone,
  addRef,
  section,
}: {
  list: api.PasskeyList
  /** Whether this browser can make a passkey. */
  supported: boolean
  adding: boolean
  onAddingDone: () => void
  /** The action that opens the form, which takes focus back when the form goes. */
  addRef: RefObject<HTMLButtonElement | null>
  section: SignInSection
}) {
  const only = list.passkeys.length === 1
  return (
    <>
      {list.passkeys.length === 0 && (
        <SettingRow label="Passkey">
          <span className="text-ink-2">
            None. You sign in with a message sent to your email address.
            {!supported && " This browser can't make passkeys."}
          </span>
        </SettingRow>
      )}
      {list.passkeys.map((passkey) => (
        <PasskeyRow
          key={passkey.id}
          passkey={passkey}
          // The last passkey of an account that signs in with passkeys only stays: the server refuses too.
          kept={only && list.passkey_only}
          last={only}
          section={section}
        />
      ))}
      {adding && (
        <AddPasskey
          taken={list.passkeys.map((passkey) => passkey.name)}
          onDone={() => {
            onAddingDone()
            addRef.current?.focus()
          }}
          section={section}
        />
      )}
    </>
  )
}

function PasskeyRow({
  passkey,
  kept,
  last,
  section,
}: {
  passkey: api.Passkey
  kept: boolean
  last: boolean
  section: SignInSection
}) {
  const [draft, setDraft] = useState<string | null>(null)
  const [removing, setRemoving] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [removeError, setRemoveError] = useState<string | null>(null)
  const renameRef = useRef<HTMLButtonElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const editing = draft !== null
  const wasEditing = useRef(false)
  const inputId = `passkey-name-${passkey.id}`

  // The field takes focus when it opens, and the action that opened it when it closes.
  useEffect(() => {
    if (editing) {
      inputRef.current?.focus()
      inputRef.current?.select()
    } else if (wasEditing.current) renameRef.current?.focus()
    wasEditing.current = editing
  }, [editing])

  async function rename(event: FormEvent) {
    event.preventDefault()
    if (pending || draft === null) return
    const name = passkeyName(draft)
    if (!name) {
      setError(badName)
      inputRef.current?.focus()
      return
    }
    if (name === passkey.name) {
      setDraft(null)
      return
    }
    setError(null)
    setPending(true)
    try {
      await api.renamePasskey(passkey.id, name)
      section.announce(`Passkey renamed to ${name}`)
      await section.refresh()
      setDraft(null)
    } catch (failure) {
      const code = section.failed(failure)
      if (!code) return
      if (code === 'not_found') {
        // Removed meanwhile, from another device: the list says so once it is read again.
        section.announce('That passkey was removed already')
        section.focusSection()
        await section.refresh()
        return
      }
      setError(code === 'invalid_name' ? badName : messageFor(code))
      inputRef.current?.focus()
    } finally {
      setPending(false)
    }
  }

  async function remove() {
    if (pending) return
    setRemoveError(null)
    setPending(true)
    try {
      await api.removePasskey(passkey.id)
    } catch (failure) {
      const code = section.failed(failure)
      if (!code) return
      if (code !== 'not_found') {
        setPending(false)
        setRemoveError(messageFor(code))
        // "Passkey only" was turned on meanwhile: the row stops offering what the server refuses.
        if (code === 'last_passkey') void section.refresh()
        return
      }
    }
    // The row goes with its passkey, so focus moves to the section it was in.
    setRemoving(false)
    section.announce(`Passkey removed: ${passkey.name}`)
    section.focusSection()
    await section.refresh()
    setPending(false)
  }

  if (editing) {
    return (
      <SettingRow label="Passkey name" labelFor={inputId}>
        <form
          noValidate
          className="flex w-full flex-wrap items-center gap-x-3 gap-y-2"
          onSubmit={rename}
        >
          <input
            id={inputId}
            ref={inputRef}
            type="text"
            autoComplete="off"
            maxLength={maxName}
            required
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            aria-invalid={error ? true : undefined}
            aria-describedby={`${inputId}-error`}
            className={field}
          />
          <button type="submit" disabled={pending} className={actionClass()}>
            Save
          </button>
          <TextAction
            disabled={pending}
            onClick={() => {
              setError(null)
              setDraft(null)
            }}
          >
            Cancel
          </TextAction>
          <p
            id={`${inputId}-error`}
            role="alert"
            className="basis-full text-small text-danger empty:hidden"
          >
            {error}
          </p>
        </form>
      </SettingRow>
    )
  }

  return (
    <>
      <SettingRow
        label="Passkey"
        action={
          <span className="flex shrink-0 gap-4">
            <TextAction ref={renameRef} onClick={() => setDraft(passkey.name)}>
              Rename<span className="sr-only"> passkey {passkey.name}</span>
            </TextAction>
            {!kept && (
              <TextAction
                onClick={() => {
                  setRemoveError(null)
                  setRemoving(true)
                }}
              >
                Remove<span className="sr-only"> passkey {passkey.name}</span>
              </TextAction>
            )}
          </span>
        }
      >
        <span className="block wrap-anywhere">{passkey.name}</span>
        <span className="block text-small text-ink-2">
          Added {formatDay(passkey.created_at)} · {passkey.synced ? 'Synced' : 'Not synced'}
        </span>
        {kept && (
          <span className="block text-small text-ink-2">
            Can't be removed: it is your only passkey, and passkey only is on.
          </span>
        )}
      </SettingRow>

      <Dialog
        open={removing}
        dismissable={!pending}
        onClose={() => setRemoving(false)}
        labelledBy={`${inputId}-remove`}
      >
        {removing && (
          <>
            <h2
              id={`${inputId}-remove`}
              tabIndex={-1}
              data-dialog-focus
              className="text-heading font-medium outline-none"
            >
              Remove this passkey?
            </h2>
            <p className="mt-2 wrap-anywhere font-medium">{passkey.name}</p>
            <p className="mt-4 text-body text-ink-2">
              It stops signing you in to Permaily now.{' '}
              {last && 'After this, you sign in with a message sent to your email address. '}
              It may stay listed on your device or in your password manager until you delete it
              there.
            </p>
            <p role="alert" className="mt-4 text-small text-danger empty:hidden">
              {removeError}
            </p>
            <div className="mt-6 flex flex-wrap gap-3">
              <button
                type="button"
                className={cx(solidDanger, pending && 'opacity-60')}
                aria-disabled={pending}
                onClick={remove}
              >
                Remove passkey
              </button>
              <Button variant="secondary" disabled={pending} onClick={() => setRemoving(false)}>
                Keep it
              </Button>
            </div>
          </>
        )}
      </Dialog>
    </>
  )
}

/** A name nobody has to think about: the browser and system the passkey is being made on, not yet taken. */
function suggestedName(taken: string[]): string {
  const base = describeDevice(navigator.userAgent)
  if (!taken.includes(base)) return base
  for (let n = 2; ; n++) {
    if (!taken.includes(`${base} ${n}`)) return `${base} ${n}`
  }
}

/** What the form says when the browser gave no passkey. A prompt the person closed is not an error. */
const problems: Record<PasskeyProblem, { quiet: boolean; text: string }> = {
  cancelled: { quiet: true, text: 'No passkey was made. You can try again.' },
  exists: {
    quiet: false,
    text: 'This device or password manager already has a passkey for this account, so it made no other.',
  },
  unavailable: { quiet: false, text: "This browser can't make a passkey here." },
  failed: { quiet: false, text: "The passkey couldn't be made. Try again." },
}

/**
 * Adding a passkey: the name first, with one suggested, then the browser's own prompt. What the browser
 * answers is kept in this form's memory only until the server has it.
 */
function AddPasskey({
  taken,
  onDone,
  section,
}: {
  taken: string[]
  onDone: () => void
  section: SignInSection
}) {
  const [name, setName] = useState(() => suggestedName(taken))
  const [pending, setPending] = useState(false)
  const [outcome, setOutcome] = useState<{ quiet: boolean; text: string } | null>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const leaving = useRef<AbortController | null>(null)
  // The browser's answer, while the server has not taken it: a name it refuses, or no network, asks for the
  // same answer again and not for another passkey.
  const made = useRef<{ ceremony: string; credential: unknown } | null>(null)

  useEffect(() => {
    inputRef.current?.focus()
    inputRef.current?.select()
    // Leaving the form closes a prompt that is still open.
    const controller = new AbortController()
    leaving.current = controller
    return () => controller.abort()
  }, [])

  // After a try that made nothing, focus is back in the form, once it can be used again.
  useEffect(() => {
    if (outcome) inputRef.current?.focus()
  }, [outcome])

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (pending) return
    const clean = passkeyName(name)
    if (!clean) {
      setOutcome({ quiet: false, text: badName })
      return
    }
    setOutcome(null)
    setPending(true)
    const signal = leaving.current?.signal
    try {
      if (!made.current) {
        const { ceremony, options } = await api.startPasskey()
        made.current = { ceremony, credential: await makePasskey(options, signal) }
      }
      const passkey = await api.finishPasskey(made.current.ceremony, clean, made.current.credential)
      made.current = null
      section.announce(`Passkey added: ${passkey.name}`)
      await section.refresh()
      if (!signal?.aborted) onDone()
    } catch (failure) {
      if (signal?.aborted) return
      setPending(false)
      if (failure instanceof PasskeyError) {
        setOutcome(problems[failure.kind])
        return
      }
      const code = section.failed(failure)
      if (!code) return
      // Only a name to fix, or no answer at all, leaves the browser's answer worth sending again.
      if (code !== 'invalid_name' && code !== 'network') made.current = null
      setOutcome({
        quiet: false,
        text:
          code === 'invalid_name'
            ? badName
            : code === 'invalid_passkey'
              ? "That passkey couldn't be added: the wait was too long, or the browser's answer didn't check. Try again."
              : messageFor(code),
      })
    }
  }

  const failed = outcome !== null && !outcome.quiet
  return (
    <SettingRow label="New passkey name" labelFor="new-passkey-name">
      <form
        noValidate
        className="flex w-full flex-wrap items-center gap-x-3 gap-y-2"
        onSubmit={submit}
      >
        <input
          id="new-passkey-name"
          ref={inputRef}
          type="text"
          autoComplete="off"
          maxLength={maxName}
          required
          value={name}
          onChange={(event) => setName(event.target.value)}
          aria-invalid={failed ? true : undefined}
          aria-describedby="new-passkey-outcome"
          className={field}
        />
        <button type="submit" disabled={pending} className={actionClass()}>
          Continue
        </button>
        <TextAction disabled={pending} onClick={onDone}>
          Cancel
        </TextAction>
        <p
          id="new-passkey-outcome"
          role={failed ? 'alert' : 'status'}
          className={cx(
            'basis-full text-small empty:hidden',
            failed ? 'text-danger' : 'text-ink-2',
          )}
        >
          {pending ? 'Waiting for your browser' : outcome?.text}
        </p>
      </form>
    </SettingRow>
  )
}
