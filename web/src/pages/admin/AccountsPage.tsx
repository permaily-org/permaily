import { type FormEvent, useCallback, useEffect, useRef, useState } from 'react'
import { Link } from 'react-router'
import { Button, SectionHeading, TextAction } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { Field } from '../auth/Field.tsx'
import { formatDay } from '../settings/signin/shared.ts'
import { adminMessage, ListState, useAdmin } from './shared.tsx'

/** An account's state in words: blocked, being deleted, or active. */
export function stateWords(record: api.AccountRecord): string {
  const closing = record.state === 'closing'
  if (record.blocked_at) return closing ? 'Blocked, being deleted' : 'Blocked'
  return closing ? 'Being deleted' : 'Active'
}

/** One account in a list: its address leads to its page. */
function AccountRow({ record }: { record: api.AccountRecord }) {
  return (
    <li className="flex flex-col gap-0.5 border-b border-rule py-2 sm:flex-row sm:items-baseline sm:gap-4">
      <Link
        to={`/admin/accounts/${encodeURIComponent(record.id)}`}
        className={cx(
          'min-w-0 flex-1 wrap-anywhere font-mono text-small text-ink underline underline-offset-4',
          focusRing,
        )}
      >
        {record.address}
      </Link>
      <span className="min-w-0 text-small text-ink-2 sm:w-40">{record.name || 'No name'}</span>
      <span className="text-small sm:w-44">{stateWords(record)}</span>
      <span className="text-small text-ink-2 sm:w-28">Since {formatDay(record.created_at)}</span>
    </li>
  )
}

/**
 * Every account, the oldest first, a page at a time, and finding one by its exact address. The address typed
 * stays in this page's memory: it is never put in the address bar.
 */
export function AccountsPage() {
  const { failed } = useAdmin()
  const [accounts, setAccounts] = useState<api.AccountRecord[] | null>(null)
  const [next, setNext] = useState<string | undefined>()
  const [unreadable, setUnreadable] = useState(0)
  const [failure, setFailure] = useState<string | null>(null)
  const [more, setMore] = useState<'idle' | 'loading' | string>('idle')
  const [typed, setTyped] = useState('')
  const [typedError, setTypedError] = useState<string | null>(null)
  const [found, setFound] = useState<
    { address: string; record: api.AccountRecord | null } | 'finding' | null
  >(null)
  const [findFailure, setFindFailure] = useState<string | null>(null)
  const fieldRef = useRef<HTMLInputElement>(null)

  const load = useCallback(async () => {
    setFailure(null)
    try {
      const page = await api.adminAccounts()
      setAccounts(page.accounts)
      setNext(page.next)
      setUnreadable(page.unreadable)
    } catch (error) {
      const code = failed(error)
      if (code) setFailure(adminMessage(code, 'account'))
    }
  }, [failed])

  useEffect(() => {
    void load()
  }, [load])

  async function loadMore() {
    if (!next || more === 'loading') return
    setMore('loading')
    try {
      const page = await api.adminAccounts(next)
      setAccounts((now) => [...(now ?? []), ...page.accounts])
      setNext(page.next)
      setUnreadable((now) => now + page.unreadable)
      setMore('idle')
    } catch (error) {
      const code = failed(error)
      setMore(code ? adminMessage(code, 'account') : 'idle')
    }
  }

  async function find(event: FormEvent) {
    event.preventDefault()
    if (found === 'finding') return
    const address = typed.trim()
    if (!address.includes('@')) {
      setTypedError('Enter the whole address of the account.')
      fieldRef.current?.focus()
      return
    }
    setTypedError(null)
    setFindFailure(null)
    setFound('finding')
    try {
      const page = await api.adminFindAccount(address)
      setFound({ address, record: page.accounts[0] ?? null })
    } catch (error) {
      setFound(null)
      const code = failed(error)
      if (code) setFindFailure(adminMessage(code, 'account'))
    }
  }

  return (
    <>
      <h1 className="text-display-s font-medium md:text-display">Accounts</h1>

      <section aria-labelledby="find-account" className="mt-8">
        <SectionHeading id="find-account" title="Find an account" />
        <form noValidate onSubmit={find} className="mt-4 flex max-w-xl flex-col gap-4">
          <Field
            id="find-address"
            label="Its address, exactly"
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
            <Button type="submit" variant="secondary" aria-disabled={found === 'finding'}>
              Find
            </Button>
          </div>
        </form>
        <div role="status" className="mt-4 empty:hidden">
          {found === 'finding' ? (
            <p className="text-small text-ink-2">Finding</p>
          ) : found ? (
            found.record ? (
              <ul aria-label="Found">
                <AccountRow record={found.record} />
              </ul>
            ) : (
              <p className="text-small text-ink-2">
                No account has the address{' '}
                <span className="wrap-anywhere font-mono">{found.address}</span>.
              </p>
            )
          ) : null}
        </div>
        <p role="alert" className="mt-4 text-small text-danger empty:hidden">
          {findFailure ? `Couldn't look for it. ${findFailure}` : ''}
        </p>
        {found !== null && found !== 'finding' && (
          <TextAction
            className="mt-3"
            onClick={() => {
              setFound(null)
              setTyped('')
              fieldRef.current?.focus()
            }}
          >
            Clear
          </TextAction>
        )}
      </section>

      <section aria-labelledby="all-accounts" className="mt-10">
        <SectionHeading id="all-accounts" title="Every account" />
        {accounts === null ? (
          <ListState failure={failure} what="The accounts" onRetry={() => void load()} />
        ) : accounts.length === 0 ? (
          <p className="border-b border-rule py-2 text-small text-ink-2">
            There are no accounts{unreadable > 0 ? ' that could be read' : ''}.
          </p>
        ) : (
          <ul aria-labelledby="all-accounts">
            {accounts.map((record) => (
              <AccountRow key={record.id} record={record} />
            ))}
          </ul>
        )}
        {unreadable > 0 && (
          <p className="py-2 text-small text-ink-2">
            {unreadable === 1
              ? "1 account couldn't be read, and isn't listed."
              : `${unreadable} accounts couldn't be read, and aren't listed.`}
          </p>
        )}
        {more !== 'idle' && more !== 'loading' && (
          <div
            role="alert"
            className="flex flex-wrap items-baseline gap-x-3 py-2 text-small text-danger"
          >
            <p>More accounts couldn't load. {more}</p>
          </div>
        )}
        {next && (
          <Button
            variant="secondary"
            size="sm"
            className="mt-4"
            aria-disabled={more === 'loading'}
            onClick={() => void loadMore()}
          >
            {more === 'loading' ? 'Loading more' : 'Show more accounts'}
          </Button>
        )}
      </section>
    </>
  )
}
