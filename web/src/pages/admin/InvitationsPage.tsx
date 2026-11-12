import { type FormEvent, useCallback, useEffect, useRef, useState } from 'react'
import { Button, Dialog, SectionHeading, TextAction } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { Field } from '../auth/Field.tsx'
import { SettingRow } from '../settings/rows.tsx'
import { formatDay } from '../settings/signin/shared.ts'
import {
  AskDialog,
  adminMessage,
  formatWhen,
  ListState,
  useAdmin,
  useAdminChange,
} from './shared.tsx'

/**
 * Making invitations, the ones not used yet, and revoking one. The codes are shown once, in this page's memory
 * only while they are on screen: the server never answers them again.
 */
export function InvitationsPage() {
  const { failed, refreshStats } = useAdmin()
  const change = useAdminChange()
  const [list, setList] = useState<{
    invitations: api.InvitationRecord[]
    truncated: boolean
  } | null>(null)
  const [failure, setFailure] = useState<string | null>(null)
  const [count, setCount] = useState('1')
  const [countError, setCountError] = useState<string | null>(null)
  const [making, setMaking] = useState(false)
  const [makeError, setMakeError] = useState<string | null>(null)
  const [made, setMade] = useState<api.InvitationsMade | null>(null)
  const [revoking, setRevoking] = useState<api.InvitationRecord | null>(null)
  const [pending, setPending] = useState(false)
  const [revokeError, setRevokeError] = useState<string | null>(null)
  const [said, setSaid] = useState('')
  const countRef = useRef<HTMLInputElement>(null)
  const listRef = useRef<HTMLElement>(null)
  // The row of a revoked invitation goes: once the question has closed, focus goes to the list.
  const toList = useRef(false)
  useEffect(() => {
    if (revoking || !toList.current) return
    toList.current = false
    listRef.current?.focus()
  }, [revoking])

  const load = useCallback(async () => {
    setFailure(null)
    try {
      setList(await api.adminInvitations())
    } catch (error) {
      const code = failed(error)
      if (code) setFailure(adminMessage(code, 'invitation'))
    }
  }, [failed])

  useEffect(() => {
    void load()
  }, [load])

  async function make(event: FormEvent) {
    event.preventDefault()
    if (making) return
    const number = /^\d+$/.test(count.trim()) ? Number(count.trim()) : 0
    if (number < 1 || number > 20) {
      setCountError('Make 1 to 20 at a time.')
      countRef.current?.focus()
      return
    }
    setCountError(null)
    setMakeError(null)
    setSaid('')
    setMaking(true)
    const result = await change(() => api.makeInvitations(number), 'invitation')
    setMaking(false)
    if ('done' in result) {
      setMade(result.done)
      refreshStats()
      void load()
      return
    }
    if (result.said) setMakeError(`No invitations were made. ${result.said}`)
  }

  async function revoke() {
    if (!revoking || pending) return
    setPending(true)
    setRevokeError(null)
    const result = await change(() => api.revokeInvitation(revoking.id), 'invitation')
    setPending(false)
    if ('done' in result) {
      toList.current = true
      setRevoking(null)
      setSaid('Invitation revoked.')
      refreshStats()
      void load()
      return
    }
    setRevokeError(result.said)
    if (result.code === 'not_found') void load()
  }

  return (
    <>
      <h1 className="text-display-s font-medium md:text-display">Invitations</h1>

      <section aria-labelledby="make-invitations" className="mt-8 max-w-3xl">
        <SectionHeading id="make-invitations" title="Make invitations" />
        <p className="mt-3 text-small text-ink-2">
          Each invitation makes one account. Its code is shown once, when it is made.
        </p>
        <form noValidate onSubmit={make} className="mt-4 flex max-w-xs flex-col gap-4">
          <Field
            id="invitation-count"
            label="How many"
            type="number"
            inputMode="numeric"
            min={1}
            max={20}
            inputRef={countRef}
            value={count}
            onChange={(event) => setCount(event.target.value)}
            error={countError}
          />
          <p role="alert" className="text-small text-danger empty:hidden">
            {makeError}
          </p>
          <div>
            <Button type="submit" aria-disabled={making}>
              Make invitations
            </Button>
          </div>
        </form>
      </section>

      <section
        ref={listRef}
        tabIndex={-1}
        aria-labelledby="unused-invitations"
        className="mt-10 max-w-3xl outline-none"
      >
        <SectionHeading id="unused-invitations" title="Not used yet" />
        <p role="status" className="py-2 text-small text-ink-2 empty:hidden">
          {said}
        </p>
        {list === null ? (
          <ListState failure={failure} what="The invitations" onRetry={() => void load()} />
        ) : list.invitations.length === 0 ? (
          <p className="border-b border-rule py-2 text-small text-ink-2">
            No invitation is waiting to be used.
          </p>
        ) : (
          list.invitations.map((invitation) => (
            <SettingRow
              key={invitation.id}
              label="Invitation"
              action={
                <TextAction
                  onClick={() => {
                    setRevokeError(null)
                    setRevoking(invitation)
                  }}
                >
                  Revoke
                  <span className="sr-only">
                    {' '}
                    invitation {invitation.id}, made {formatWhen(invitation.created_at)}
                  </span>
                </TextAction>
              }
            >
              <span className="block">Made {formatWhen(invitation.created_at)}</span>
              {/* The identifier matches the row to the code shown when it was made, and to Activity. */}
              <span className="block wrap-anywhere font-mono text-small text-ink-2">
                {invitation.id}
              </span>
              <span className="block text-small text-ink-2">
                Works until {formatDay(invitation.expires_at)}
              </span>
            </SettingRow>
          ))
        )}
        {list?.truncated && (
          <p className="py-2 text-small text-ink-2">Not every unused invitation is listed here.</p>
        )}
      </section>

      <AskDialog
        id="revoke-invitation"
        open={revoking !== null}
        title="Revoke this invitation?"
        subject={revoking ? `${revoking.id}, made ${formatWhen(revoking.created_at)}` : undefined}
        error={revokeError}
        pending={pending}
        action="Revoke invitation"
        danger
        keep="Keep it"
        onAct={() => void revoke()}
        onClose={() => setRevoking(null)}
      >
        Whoever has its code can no longer make an account with it.
      </AskDialog>

      <Dialog
        open={made !== null}
        dismissable={false}
        onClose={() => setMade(null)}
        labelledBy="invitations-made"
      >
        {made && <MadeCodes made={made} onDone={() => setMade(null)} />}
      </Dialog>
    </>
  )
}

/** The codes just made, this once, with a way to copy them. */
function MadeCodes({ made, onDone }: { made: api.InvitationsMade; onDone: () => void }) {
  const [copied, setCopied] = useState<string | null>(null)
  const count = made.invitations.length

  // Closing or reloading the tab would lose the codes for good: the browser asks first.
  useEffect(() => {
    const ask = (event: BeforeUnloadEvent) => event.preventDefault()
    window.addEventListener('beforeunload', ask)
    return () => window.removeEventListener('beforeunload', ask)
  }, [])

  async function copy() {
    try {
      await navigator.clipboard.writeText(made.invitations.join('\n'))
      setCopied(count === 1 ? 'Copied the invitation.' : `Copied ${count} invitations.`)
    } catch {
      setCopied("Couldn't copy. Select the codes and copy them by hand.")
    }
  }

  const until = made.expires_at ? formatDay(made.expires_at) : ''
  return (
    <>
      <h2
        id="invitations-made"
        tabIndex={-1}
        data-dialog-focus
        className="text-heading font-medium outline-none"
      >
        {count === 1 ? 'Your new invitation' : `Your ${count} new invitations`}
      </h2>
      <p className="mt-3 text-body">
        <strong className="font-medium">
          {count === 1 ? "This code won't be shown again." : "These codes won't be shown again."}
        </strong>{' '}
        Copy {count === 1 ? 'it' : 'them'} now. Each one makes one account
        {until ? `, until ${until}` : ''}.
      </p>
      <p className="mt-3 text-small text-ink-2">
        Next to each code is its identifier, which the list of unused invitations and Activity show.
      </p>
      <ol className="mt-3 flex flex-col gap-1 border-y border-rule py-3 font-mono">
        {made.invitations.map((code, n) => (
          <li key={code} className="flex flex-wrap items-baseline gap-x-4">
            <span className="text-body">{code}</span>
            {made.ids[n] && (
              <span className="wrap-anywhere text-small text-ink-2">{made.ids[n]}</span>
            )}
          </li>
        ))}
      </ol>
      <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2">
        <Button variant="secondary" size="sm" onClick={copy}>
          Copy all
        </Button>
        <p role="status" className="text-small text-ink-2 empty:hidden">
          {copied}
        </p>
      </div>
      <div className="mt-6 flex flex-wrap gap-3">
        <Button onClick={onDone}>Done</Button>
      </div>
    </>
  )
}
