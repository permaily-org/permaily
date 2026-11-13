import { useCallback, useEffect, useState } from 'react'
import { Navigate } from 'react-router'
import { ButtonLink, TextLink } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'
import { pagePadding } from '../../lib/styles.ts'
import { LoadState } from '../accounts/shared.tsx'
import { canChange, useStanding } from '../accounts/standing.ts'
import { messageFor } from '../auth/messages.ts'

/**
 * `/inbox`: the inbox of the first mailbox that can be read. Without one, it says so and offers to connect
 * one, to an account that may.
 */
export function InboxIndexPage() {
  const { failed } = useSession()
  const standing = useStanding()
  const [list, setList] = useState<api.Mailbox[] | null>(null)
  const [failure, setFailure] = useState<string | null>(null)

  const load = useCallback(async () => {
    setFailure(null)
    try {
      setList((await api.mailboxes()).mailboxes)
    } catch (error) {
      const code = failed(error)
      if (code) setFailure(messageFor(code))
    }
  }, [failed])

  useEffect(() => {
    void load()
  }, [load])

  const first = list?.find((mailbox) => mailbox.state !== 'unreadable')
  if (first) return <Navigate to={`/inbox/${encodeURIComponent(first.id)}`} replace />
  return (
    <div className={pagePadding}>
      <h1 className="text-display-s font-medium md:text-display">Inbox</h1>
      <div className="mt-6 max-w-2xl border-t border-rule-strong">
        {list === null ? (
          <LoadState failure={failure} what="Your mailboxes" onRetry={() => void load()} />
        ) : list.length > 0 ? (
          <p className="py-5 text-body text-ink-2">
            Your mailboxes can't be read. See them in <TextLink to="/accounts">Accounts</TextLink>.
          </p>
        ) : (
          <div className="py-5">
            <p className="text-body text-ink-2">
              No mailbox yet. Mail you forward from Gmail to a mailbox of your own shows here.
            </p>
            {canChange(standing) && (
              <ButtonLink to="/accounts/new" className="mt-5">
                Connect a mailbox
              </ButtonLink>
            )}
          </div>
        )}
      </div>
    </div>
  )
}
