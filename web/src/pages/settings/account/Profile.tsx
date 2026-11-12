import { type FormEvent, useEffect, useRef, useState } from 'react'
import { SectionHeading, TextAction } from '../../../components/ui/index.ts'
import * as api from '../../../lib/api.ts'
import { useSession } from '../../../lib/session.tsx'
import { actionClass } from '../../../lib/styles.ts'
import { messageFor } from '../../auth/messages.ts'
import { field, SettingRow } from '../rows.tsx'
import { ContactAddress } from './ContactAddress.tsx'

/** The most characters the server keeps of an account's name. */
const maxName = 80

/**
 * The name as the server keeps it, or why it cannot keep it. An empty one is kept: it takes the name away.
 * The server checks the same and has the last word.
 */
function accountName(typed: string): { name: string } | { problem: string } {
  const name = typed.trim()
  const length = [...name].length
  if (length > maxName) {
    return { problem: `That name has ${length} characters. Use ${maxName} or fewer.` }
  }
  // Letters, marks, digits, punctuation, symbols and the plain space: what the server takes for one line.
  if (!/^[\p{L}\p{M}\p{N}\p{P}\p{S} ]*$/u.test(name)) {
    return {
      problem: "That name has a character Permaily can't keep, such as a tab. Type it again.",
    }
  }
  return { name }
}

const refused = `Permaily can't keep that name. Use one line of ${maxName} characters or fewer.`

/** The language's own name in English, as the browser knows it: "English" for `en`. */
function languageName(tag: string): string {
  try {
    return new Intl.DisplayNames('en', { type: 'language' }).of(tag) ?? 'English'
  } catch {
    return 'English'
  }
}

/**
 * The account's own facts, as the server has them: the name, which the person changes or takes away, the
 * contact address, which is changed with a code sent to the new one (ContactAddress.tsx), and the language.
 */
export function Profile() {
  const { state, accountChanged, failed } = useSession()
  const [draft, setDraft] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [rowError, setRowError] = useState<string | null>(null)
  const [said, setSaid] = useState('')
  const editRef = useRef<HTMLButtonElement>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const editing = draft !== null
  const wasEditing = useRef(false)
  // The action that took the name away goes with it: focus moves to the one that is left. It is not disabled
  // while the request is on its way, since a disabled control drops the focus it has.
  const focusEdit = useRef(false)

  // The field takes focus when it opens, and the action that opened it when it closes.
  useEffect(() => {
    if (editing) {
      inputRef.current?.focus()
      inputRef.current?.select()
    } else if (wasEditing.current) editRef.current?.focus()
    wasEditing.current = editing
  }, [editing])

  const account = state.status === 'signedIn' ? state.me.account : null
  const name = account?.name ?? ''

  // biome-ignore lint/correctness/useExhaustiveDependencies: runs when the row is drawn with the new name
  useEffect(() => {
    if (!focusEdit.current) return
    focusEdit.current = false
    editRef.current?.focus()
  }, [name])

  if (!account) return null

  /** Sends the name. True when the server took it; what went wrong is said where `tell` puts it. */
  async function send(next: string, tell: (message: string) => void): Promise<boolean> {
    setPending(true)
    try {
      const changed = await api.renameAccount(next)
      setSaid(changed.name ? `Name changed to ${changed.name}` : 'Name removed')
      accountChanged(changed)
      return true
    } catch (failure) {
      const code = failed(failure)
      if (code) tell(code === 'invalid_name' ? refused : messageFor(code))
      return false
    } finally {
      setPending(false)
    }
  }

  async function save(event: FormEvent) {
    event.preventDefault()
    if (pending || draft === null) return
    const checked = accountName(draft)
    if ('problem' in checked) {
      setError(checked.problem)
      inputRef.current?.focus()
      return
    }
    if (checked.name === name) {
      setDraft(null)
      return
    }
    setError(null)
    if (await send(checked.name, setError)) setDraft(null)
    else inputRef.current?.focus()
  }

  async function remove() {
    if (pending) return
    setRowError(null)
    focusEdit.current = true
    if (!(await send('', setRowError))) focusEdit.current = false
  }

  return (
    <section aria-labelledby="profile">
      <SectionHeading id="profile" title="Profile" />
      {editing ? (
        <SettingRow label="Name" labelFor="account-name">
          <form
            noValidate
            className="flex w-full flex-wrap items-center gap-x-3 gap-y-2"
            onSubmit={save}
          >
            <input
              id="account-name"
              ref={inputRef}
              type="text"
              autoComplete="name"
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              aria-invalid={error ? true : undefined}
              aria-describedby="account-name-error"
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
              id="account-name-error"
              role="alert"
              className="basis-full text-small text-danger empty:hidden"
            >
              {error}
            </p>
          </form>
        </SettingRow>
      ) : (
        <SettingRow
          label="Name"
          action={
            <span className="flex shrink-0 gap-4">
              <TextAction
                ref={editRef}
                onClick={() => {
                  if (pending) return
                  setError(null)
                  setRowError(null)
                  setDraft(name)
                }}
              >
                {name ? 'Edit' : 'Add'}
                <span className="sr-only">{name ? ' name' : ' a name'}</span>
              </TextAction>
              {name && (
                <TextAction aria-disabled={pending} onClick={remove}>
                  Remove<span className="sr-only"> name</span>
                </TextAction>
              )}
            </span>
          }
        >
          {name ? (
            <span className="block wrap-anywhere">{name}</span>
          ) : (
            <span className="text-ink-2">None</span>
          )}
          <span role="alert" className="block text-small text-danger empty:hidden">
            {rowError}
          </span>
        </SettingRow>
      )}
      <ContactAddress account={account} />
      <SettingRow label="Language">{languageName(account.language)}</SettingRow>
      <p role="status" className="sr-only">
        {said}
      </p>
    </section>
  )
}
