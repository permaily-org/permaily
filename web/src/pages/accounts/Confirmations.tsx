import type * as api from '../../lib/api.ts'
import { cx } from '../../lib/styles.ts'
import { CopyButton, formatWhen } from './shared.tsx'

/**
 * One forwarding confirmation Gmail sent: the link to confirm (as Gmail sends it since 2026) or the code to copy
 * into Gmail, the Gmail account that asked, when, and whether Google confirmed it. One for another address shows
 * neither. The link is text to copy, never a link of the app: the server keeps it only when it is Gmail's own
 * confirmation page (`https://mail-settings.google.com/mail/vf-…`), Permaily never follows it, and the person opens
 * it in the browser where they use Gmail, having read which account asked (ADR 0026).
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
  const { requester, code, link } = confirmation
  return (
    <div className="border-b border-rule py-3">
      {code && (
        <div className="flex flex-wrap items-baseline gap-x-4 gap-y-1">
          <span className="font-mono text-title font-medium tracking-wide">{code}</span>
          <CopyButton value={code} what="the code" />
        </div>
      )}
      <p className={cx('text-small text-ink-2', code && 'mt-1')}>
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
      {link && (
        <>
          {confirmation.verified && (
            <p className="mt-1 text-body">
              Google sent a link to confirm it. Copy it into the browser where you use Gmail.
            </p>
          )}
          <div className="mt-2 flex flex-wrap items-baseline gap-x-4 gap-y-1 bg-sunken px-3 py-2.5">
            <span className="min-w-0 font-mono text-small wrap-anywhere select-all">
              <bdi dir="ltr">{link}</bdi>
            </span>
            <CopyButton value={link} what="the link" />
          </div>
          {requester && (
            <p className="mt-1 text-body">
              Open this link only if <span className="font-mono wrap-anywhere">{requester}</span> is
              your account.
            </p>
          )}
        </>
      )}
      {code && requester && (
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
