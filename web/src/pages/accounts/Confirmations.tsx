import type * as api from '../../lib/api.ts'
import { cx } from '../../lib/styles.ts'
import { CopyButton, formatWhen } from './shared.tsx'

/**
 * One forwarding confirmation Gmail sent: its code to copy into Gmail, the Gmail account that asked, when, and
 * whether Google confirmed it. Its link is never shown: Permaily never follows it, and neither should anyone
 * who did not ask. One for another address shows no code.
 */
export function Confirmation({ confirmation }: { confirmation: api.ForwardingConfirmation }) {
  const when = formatWhen(confirmation.received_at)
  if (confirmation.for_another_address) {
    return (
      <div className="border-b border-rule py-3">
        <p className="text-body">
          Gmail sent this for another address. It says nothing about this mailbox.
        </p>
        <p className="mt-1 font-mono text-mono text-ink-2">{when}</p>
      </div>
    )
  }
  const requester = confirmation.requester
  return (
    <div className="border-b border-rule py-3">
      <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
        <span className="font-mono text-title font-medium tracking-wide">{confirmation.code}</span>
        <CopyButton value={confirmation.code} what="the code" />
      </div>
      <p className="mt-1 text-small text-ink-2">
        {requester ? (
          <>
            Asked by <span className="font-mono text-ink wrap-anywhere">{requester}</span>,{' '}
          </>
        ) : null}
        <span className="font-mono">{when}</span>
      </p>
      <p className={cx('mt-2 text-body', confirmation.verified ? 'text-ok' : 'text-attention')}>
        {confirmation.verified
          ? 'Google confirmed this request.'
          : "Not confirmed by Google: if you didn't ask Gmail to forward here, ignore it."}
      </p>
      {requester && (
        <p className="mt-1 text-body">
          Copy this code into Gmail only if{' '}
          <span className="font-mono wrap-anywhere">{requester}</span> is your account.
        </p>
      )}
    </div>
  )
}

/** The confirmations of a mailbox, the newest first, as the server sends them. */
export function Confirmations({ confirmations }: { confirmations: api.ForwardingConfirmation[] }) {
  return (
    <ul aria-label="Gmail's confirmations" className="border-t border-rule">
      {confirmations.map((confirmation) => (
        <li key={`${confirmation.received_at} ${confirmation.code} ${confirmation.message ?? ''}`}>
          <Confirmation confirmation={confirmation} />
        </li>
      ))}
    </ul>
  )
}
