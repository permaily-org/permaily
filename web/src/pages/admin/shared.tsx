import { createContext, type ReactNode, useContext } from 'react'
import { useConfirmed } from '../../components/Confirm.tsx'
import { Button, Dialog, TextAction } from '../../components/ui/index.ts'
import type { ApiErrorCode } from '../../lib/api.ts'
import { isCancelled } from '../../lib/cancelled.ts'
import { formatClock } from '../../lib/closing.ts'
import { cx } from '../../lib/styles.ts'
import { solidDanger } from '../accounts/styles.ts'
import { messageFor } from '../auth/messages.ts'
import { formatDay } from '../settings/signin/shared.ts'

/** What every page of the admin panel needs from the panel around it. */
export type AdminPanel = {
  /** True for the super admin, who alone sets providers and makes or removes admins. */
  superAdmin: boolean
  /** The id of the admin's own account. */
  self: string
  /** Asks for the numbers at the top again, after something changed. */
  refreshStats: () => void
  /**
   * The code of a failure, or null when there is nothing to say where it failed: the session ended, the
   * account is being deleted, or the account no longer signs in with passkeys only, and the panel says that.
   */
  failed: (error: unknown) => ApiErrorCode | null
}

export const AdminContext = createContext<AdminPanel | null>(null)

export function useAdmin(): AdminPanel {
  const panel = useContext(AdminContext)
  if (!panel) throw new Error('useAdmin needs the admin panel around it')
  return panel
}

/** What a refusal is about, for the words that say it. */
export type About = 'account' | 'invitation' | 'provider' | 'admin'

const superOnly: Record<About, string> = {
  account: "Only the super admin can act on an admin's account.",
  invitation: 'Only the super admin can do that.',
  provider: 'Only the super admin can set up providers.',
  admin: 'Only the super admin can add or remove admins.',
}

const gone: Record<About, string> = {
  account: "That account isn't there any more.",
  invitation: 'That invitation was used, revoked or never made.',
  provider: "That isn't there any more.",
  admin: "That account isn't there any more.",
}

/** The fixed words for each refusal of the admin API. */
export function adminMessage(code: ApiErrorCode, about: About): string {
  if (code === 'super_admin_only') return superOnly[about]
  if (code === 'not_found') return gone[about]
  return messageFor(code)
}

/** "6 Mar 2026, 14:32 UTC", in the person's own time zone. */
export function formatWhen(iso: string): string {
  const day = formatDay(iso)
  return day ? `${day}, ${formatClock(new Date(iso))}` : ''
}

/**
 * Runs an admin's change: it confirms the person first when the server asks, and answers what to say when it
 * failed, or null when there is nothing to say (the person chose not to confirm, or the panel says it).
 */
export function useAdminChange() {
  const confirmed = useConfirmed()
  const { failed } = useAdmin()
  return async function change<T>(
    action: () => Promise<T>,
    about: About,
  ): Promise<{ done: T } | { said: string | null; code: ApiErrorCode | null }> {
    try {
      return { done: await confirmed(action) }
    } catch (error) {
      if (isCancelled(error)) return { said: null, code: null }
      const code = failed(error)
      return { said: code ? adminMessage(code, about) : null, code }
    }
  }
}

/** A question before a change: what it is done to, and what happens, in one sentence. */
export function AskDialog({
  id,
  open,
  title,
  subject,
  children,
  error,
  pending,
  action,
  danger = false,
  keep = 'Not now',
  onAct,
  onClose,
}: {
  id: string
  open: boolean
  title: string
  /** What the change is done to, such as the account's address, in monospace. */
  subject?: string
  children: ReactNode
  error: string | null
  pending: boolean
  action: string
  danger?: boolean
  keep?: string
  onAct: () => void
  onClose: () => void
}) {
  return (
    <Dialog open={open} dismissable={!pending} onClose={onClose} labelledBy={id}>
      {open && (
        <>
          {/* The question takes focus, not an action: Enter must not answer it unread. */}
          <h2
            id={id}
            tabIndex={-1}
            data-dialog-focus
            className="text-heading font-medium outline-none"
          >
            {title}
          </h2>
          {subject && <p className="mt-2 wrap-anywhere font-mono text-small">{subject}</p>}
          <p className="mt-4 text-body text-ink-2">{children}</p>
          <p role="alert" className="mt-4 text-small text-danger empty:hidden">
            {error}
          </p>
          <div className="mt-6 flex flex-wrap gap-3">
            {danger ? (
              <button
                type="button"
                className={cx(solidDanger, pending && 'opacity-60')}
                aria-disabled={pending}
                onClick={() => !pending && onAct()}
              >
                {action}
              </button>
            ) : (
              <Button aria-disabled={pending} onClick={() => !pending && onAct()}>
                {action}
              </Button>
            )}
            <Button variant="secondary" disabled={pending} onClick={onClose}>
              {keep}
            </Button>
          </div>
        </>
      )}
    </Dialog>
  )
}

/** How a list stands before it has rows: loading, or failed with a way to try again. */
export function ListState({
  failure,
  what,
  onRetry,
}: {
  failure: string | null
  /** What could not load, such as "The accounts". */
  what: string
  onRetry: () => void
}) {
  if (!failure) {
    return (
      <p role="status" className="border-b border-rule py-2 text-small text-ink-2">
        Loading
      </p>
    )
  }
  return (
    <div
      role="alert"
      className="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-rule py-2 text-small text-danger"
    >
      <p>
        {what} couldn't load. {failure}
      </p>
      <TextAction onClick={onRetry}>Try again</TextAction>
    </div>
  )
}
