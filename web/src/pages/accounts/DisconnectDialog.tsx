import { Button, Dialog } from '../../components/ui/index.ts'
import type * as api from '../../lib/api.ts'
import { cx } from '../../lib/styles.ts'

/**
 * What disconnecting does, in words that say what the server does and nothing stronger (ADR 0026, "What
 * disconnecting does to the key"; docs/web.md, "Disconnecting").
 */
export const disconnectWords = [
  'Its address stops accepting mail now, and nobody can open its mail in Permaily any more.',
  "Permaily deletes the messages it holds, usually within minutes. Mail that was arriving at that moment can't be opened and expires on its own.",
  'Gmail keeps its own copy of your mail, and so does anything you downloaded.',
  "Permaily's code never opens this mailbox's key again, but the key isn't destroyed: a copy of Permaily's storage saved before now could still be opened by someone who also got Permaily's root secret, which only Permaily's published servers hold. A record that a mailbox with this number existed, with times and fingerprints and no content, stays in a locked log for ten years.",
  'Turn forwarding to it off in Gmail, or Gmail will tell you forwarding fails.',
]

/**
 * Asks before a mailbox is disconnected. Both actions have the same weight, and the question takes focus, so
 * Enter disconnects nothing unread.
 */
export function DisconnectDialog({
  mailbox,
  pending,
  error,
  onCancel,
  onConfirm,
}: {
  mailbox: api.Mailbox | null
  pending: boolean
  error: string | null
  onCancel: () => void
  onConfirm: (mailbox: api.Mailbox) => void
}) {
  return (
    <Dialog
      open={mailbox !== null}
      dismissable={!pending}
      onClose={onCancel}
      labelledBy="disconnect-title"
    >
      {mailbox && (
        <>
          <h2
            id="disconnect-title"
            tabIndex={-1}
            data-dialog-focus
            className="text-heading font-medium outline-none"
          >
            Disconnect this mailbox?
          </h2>
          {(mailbox.label || mailbox.address) && (
            <p className="mt-2 text-small">
              {mailbox.label && <span className="font-medium">{mailbox.label} </span>}
              <span className="font-mono wrap-anywhere text-ink-2">{mailbox.address}</span>
            </p>
          )}
          <div className="mt-5 flex flex-col gap-3 text-body">
            {disconnectWords.map((words, i) => (
              <p key={words} className={cx(i > 0 && 'text-ink-2')}>
                {words}
              </p>
            ))}
          </div>
          <p role="alert" className="mt-4 text-small text-danger empty:hidden">
            {error}
          </p>
          <div className="mt-6 flex flex-wrap gap-3">
            {/* Never `disabled`: focus comes back here after confirming it's you (web/AGENTS.md). */}
            <Button
              variant="secondary"
              aria-disabled={pending}
              onClick={() => !pending && onConfirm(mailbox)}
            >
              Disconnect and delete mail
            </Button>
            <Button variant="secondary" disabled={pending} onClick={onCancel}>
              Keep it
            </Button>
          </div>
        </>
      )}
    </Dialog>
  )
}
