import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router'
import { Button } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { cx, focusRing } from '../../lib/styles.ts'
import { adminMessage, formatWhen, ListState, useAdmin } from './shared.tsx'

/** What each kind of entry says. A kind this app does not know is shown as it is. */
const kinds: Record<string, string> = {
  block: 'Blocked an account',
  unblock: 'Unblocked an account',
  delete: 'Asked to delete an account',
  delete_cancel: 'Cancelled the deletion of an account',
  sessions_close: 'Signed out every device of an account',
  // One entry for each invitation, also when several were made at once.
  invitations_made: 'Made an invitation',
  invitation_revoked: 'Revoked an invitation',
  provider_app_set: "Set up a provider's application",
  provider_app_removed: "Removed a provider's application",
  admin_made: 'Made an admin',
  admin_removed: 'Removed an admin',
}

/** What the admins did, the newest first, a page at a time. */
export function LogPage() {
  const { failed } = useAdmin()
  const [entries, setEntries] = useState<api.LogEntry[] | null>(null)
  const [next, setNext] = useState<string | undefined>()
  const [failure, setFailure] = useState<string | null>(null)
  const [more, setMore] = useState<'idle' | 'loading' | string>('idle')

  const load = useCallback(async () => {
    setFailure(null)
    try {
      const page = await api.adminLog()
      setEntries(page.entries)
      setNext(page.next)
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
      const page = await api.adminLog(next)
      setEntries((now) => [...(now ?? []), ...page.entries])
      setNext(page.next)
      setMore('idle')
    } catch (error) {
      const code = failed(error)
      setMore(code ? adminMessage(code, 'account') : 'idle')
    }
  }

  return (
    <>
      <h1 className="text-display-s font-medium md:text-display">Admin activity</h1>
      <section aria-label="What admins did" className="mt-8 max-w-3xl">
        {entries === null ? (
          <ListState failure={failure} what="The activity" onRetry={() => void load()} />
        ) : entries.length === 0 ? (
          <p className="border-y border-rule py-2 text-small text-ink-2">
            Nothing has been done here yet.
          </p>
        ) : (
          <ol className="border-t border-rule">
            {entries.map((entry, n) => (
              <li
                // Entries have no id of their own, and several can share a time: the list only grows at its
                // end, so the place names one.
                // biome-ignore lint/suspicious/noArrayIndexKey: see above
                key={n}
                className="flex flex-col gap-0.5 border-b border-rule py-2 sm:flex-row sm:gap-4"
              >
                <span className="shrink-0 font-mono text-small text-ink-2 sm:w-52">
                  {formatWhen(entry.at)}
                </span>
                <span className="min-w-0 flex-1">
                  <span className="block">
                    {kinds[entry.kind] ?? (
                      <span className="font-mono text-small">{entry.kind}</span>
                    )}
                  </span>
                  {entry.account && (
                    <span className="block text-small text-ink-2">
                      {entry.account.address ? (
                        <Link
                          to={`/admin/accounts/${encodeURIComponent(entry.account.id)}`}
                          className={cx(
                            'wrap-anywhere font-mono underline underline-offset-4',
                            focusRing,
                          )}
                        >
                          {entry.account.address}
                        </Link>
                      ) : (
                        <>
                          An account that is gone,{' '}
                          <span className="wrap-anywhere font-mono">{entry.account.id}</span>
                        </>
                      )}
                    </span>
                  )}
                  {entry.invitation && (
                    <span className="block text-small text-ink-2">
                      Invitation <span className="wrap-anywhere font-mono">{entry.invitation}</span>
                    </span>
                  )}
                  <span className="block text-small text-ink-2">
                    By <span className="wrap-anywhere font-mono">{entry.admin.address}</span>
                  </span>
                </span>
              </li>
            ))}
          </ol>
        )}
        {more !== 'idle' && more !== 'loading' && (
          <p role="alert" className="py-2 text-small text-danger">
            Older activity couldn't load. {more}
          </p>
        )}
        {next && (
          <Button
            variant="secondary"
            size="sm"
            className="mt-4"
            aria-disabled={more === 'loading'}
            onClick={() => void loadMore()}
          >
            {more === 'loading' ? 'Loading more' : 'Show older activity'}
          </Button>
        )}
      </section>
    </>
  )
}
