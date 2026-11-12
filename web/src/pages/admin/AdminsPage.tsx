import { type FormEvent, useCallback, useEffect, useRef, useState } from 'react'
import { Button, SectionHeading, TextAction } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { Field } from '../auth/Field.tsx'
import { SettingRow } from '../settings/rows.tsx'
import { formatDay } from '../settings/signin/shared.ts'
import { AskDialog, adminMessage, ListState, useAdmin, useAdminChange } from './shared.tsx'

/** The admins. The super admin adds one by finding an account by its address, and removes one. */
export function AdminsPage() {
  const { failed, superAdmin } = useAdmin()
  const change = useAdminChange()
  const [list, setList] = useState<api.AdminRecord[] | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [removing, setRemoving] = useState<api.AdminRecord | null>(null)
  const [pending, setPending] = useState(false)
  const [removeError, setRemoveError] = useState<string | null>(null)
  const [said, setSaid] = useState('')
  const listRef = useRef<HTMLElement>(null)
  const toList = useRef(false)

  const load = useCallback(async () => {
    setFailure(null)
    try {
      setList(await api.admins())
    } catch (error) {
      const code = failed(error)
      if (code) setFailure(adminMessage(code, 'admin'))
    }
  }, [failed])

  useEffect(() => {
    void load()
  }, [load])

  // The row of a removed admin goes: once the question has closed, focus goes to the list.
  useEffect(() => {
    if (removing || !toList.current) return
    toList.current = false
    listRef.current?.focus()
  }, [removing])

  async function remove() {
    if (!removing || pending) return
    setPending(true)
    setRemoveError(null)
    const one = removing
    const result = await change(() => api.removeAdmin(one.id), 'admin')
    setPending(false)
    if ('done' in result) {
      toList.current = true
      setRemoving(null)
      setSaid(`${one.address} is no longer an admin.`)
      setList(result.done)
      return
    }
    setRemoveError(result.said)
  }

  return (
    <>
      <h1 className="text-display-s font-medium md:text-display">Admins</h1>
      <section
        ref={listRef}
        tabIndex={-1}
        aria-labelledby="admin-list"
        className="mt-8 max-w-3xl outline-none"
      >
        <SectionHeading id="admin-list" title="Who is an admin" />
        <p className="mt-3 text-small text-ink-2">
          {superAdmin
            ? 'Admins can act on accounts and make invitations, while their own account signs in with passkeys only.'
            : 'Only the super admin can add or remove admins.'}
        </p>
        <p role="status" className="py-2 text-small text-ink-2 empty:hidden">
          {said}
        </p>
        {list === null ? (
          <ListState failure={failure} what="The admins" onRetry={() => void load()} />
        ) : list.length === 0 ? (
          <p className="border-b border-rule py-2 text-small text-ink-2">There are no admins.</p>
        ) : (
          list.map((one) => (
            <SettingRow
              key={one.id}
              label={one.super ? 'Super admin' : 'Admin'}
              action={
                superAdmin && !one.super ? (
                  <TextAction
                    tone="danger"
                    onClick={() => {
                      setRemoveError(null)
                      setRemoving(one)
                    }}
                  >
                    Remove<span className="sr-only"> {one.address}</span>
                  </TextAction>
                ) : undefined
              }
            >
              <span className="block wrap-anywhere font-mono text-small">{one.address}</span>
              {(one.name || one.since) && (
                <span className="block text-small text-ink-2">
                  {[one.name, one.since ? `Admin since ${formatDay(one.since)}` : '']
                    .filter(Boolean)
                    .join(' · ')}
                </span>
              )}
            </SettingRow>
          ))
        )}
      </section>

      {superAdmin && <AddAdmin admins={list} onAdded={setList} />}

      <AskDialog
        id="remove-admin"
        open={removing !== null}
        title="Remove this admin?"
        subject={removing?.address}
        error={removeError}
        pending={pending}
        action="Remove admin"
        danger
        keep="Keep them"
        onAct={() => void remove()}
        onClose={() => setRemoving(null)}
      >
        They keep their account, and can no longer use the admin panel.
      </AskDialog>
    </>
  )
}

function AddAdmin({
  admins,
  onAdded,
}: {
  admins: api.AdminRecord[] | null
  /** Takes the admins as the server answered the change. */
  onAdded: (admins: api.AdminRecord[]) => void
}) {
  const { failed } = useAdmin()
  const change = useAdminChange()
  const [typed, setTyped] = useState('')
  const [typedError, setTypedError] = useState<string | null>(null)
  const [found, setFound] = useState<{ address: string; record: api.AccountRecord | null } | null>(
    null,
  )
  const [finding, setFinding] = useState(false)
  const [findError, setFindError] = useState<string | null>(null)
  const [asking, setAsking] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [said, setSaid] = useState('')
  const fieldRef = useRef<HTMLInputElement>(null)

  async function find(event: FormEvent) {
    event.preventDefault()
    if (finding) return
    const address = typed.trim()
    if (!address.includes('@')) {
      setTypedError('Enter the whole address of the account.')
      fieldRef.current?.focus()
      return
    }
    setTypedError(null)
    setFindError(null)
    setSaid('')
    setFinding(true)
    try {
      const page = await api.adminFindAccount(address)
      setFound({ address, record: page.accounts[0] ?? null })
    } catch (failure) {
      setFound(null)
      const code = failed(failure)
      if (code) setFindError(`Couldn't look for it. ${adminMessage(code, 'admin')}`)
    } finally {
      setFinding(false)
    }
  }

  async function make() {
    const record = found?.record
    if (!record || pending) return
    setPending(true)
    setError(null)
    const result = await change(() => api.makeAdmin(record.id), 'admin')
    setPending(false)
    if ('done' in result) {
      setAsking(false)
      setFound(null)
      setTyped('')
      setSaid(`${record.address} is an admin now.`)
      fieldRef.current?.focus()
      onAdded(result.done)
      return
    }
    setError(result.said)
  }

  const record = found?.record ?? null
  const already =
    record !== null && (record.admin !== undefined || admins?.some((one) => one.id === record.id))
  return (
    <section aria-labelledby="add-admin" className="mt-10 max-w-3xl">
      <SectionHeading id="add-admin" title="Add an admin" />
      <form noValidate onSubmit={find} className="mt-4 flex max-w-xl flex-col gap-4">
        <Field
          id="admin-address"
          label="The address of their account"
          type="email"
          autoComplete="off"
          autoCapitalize="none"
          autoCorrect="off"
          spellCheck={false}
          inputRef={fieldRef}
          value={typed}
          onChange={(event) => setTyped(event.target.value)}
          error={typedError}
        />
        <div>
          <Button type="submit" variant="secondary" aria-disabled={finding}>
            Find
          </Button>
        </div>
      </form>
      <p role="alert" className="mt-4 text-small text-danger empty:hidden">
        {findError}
      </p>
      <div role="status" className="mt-4 empty:hidden">
        {finding ? (
          <p className="text-small text-ink-2">Finding</p>
        ) : said ? (
          <p className="text-small text-ink-2">{said}</p>
        ) : found && !record ? (
          <p className="text-small text-ink-2">
            No account has the address{' '}
            <span className="wrap-anywhere font-mono">{found.address}</span>.
          </p>
        ) : record ? (
          <div className="flex flex-col items-start gap-3 border-y border-rule py-3">
            <p>
              <span className="block wrap-anywhere font-mono text-small">{record.address}</span>
              {record.name && <span className="block text-small text-ink-2">{record.name}</span>}
            </p>
            {already ? (
              <p className="text-small text-ink-2">That account is an admin already.</p>
            ) : (
              <Button
                size="sm"
                onClick={() => {
                  setError(null)
                  setAsking(true)
                }}
              >
                Make admin
              </Button>
            )}
          </div>
        ) : null}
      </div>

      <AskDialog
        id="make-admin"
        open={asking}
        title="Make this account an admin?"
        subject={record?.address}
        error={error}
        pending={pending}
        action="Make admin"
        onAct={() => void make()}
        onClose={() => setAsking(false)}
      >
        They can block, delete and sign out accounts and make invitations, while their own account
        signs in with passkeys only.
      </AskDialog>
    </section>
  )
}
