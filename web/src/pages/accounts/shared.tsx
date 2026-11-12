import { useState } from 'react'
import { TextAction, TextAnchor } from '../../components/ui/index.ts'
import type * as api from '../../lib/api.ts'
import { formatClock } from '../../lib/closing.ts'
import { supportAddress } from '../auth/messages.ts'
import { formatDay } from '../settings/signin/shared.ts'

/**
 * What the person is told when Disconnect answers `mailbox_unreadable`: the mailbox's record was forked and it
 * cannot be disconnected. The words of `contracts.ErrMailboxForked` (internal/contracts/mailboxes.go), which a
 * test compares, as ADR 0026 says them ("An unreadable mailbox").
 */
export const forkedWords =
  "This mailbox can't be disconnected: what Permaily stores for it was changed by someone else. It accepts no mail and nobody can open it. Write to support@permaily.com"

/** "12 Nov 2026, 14:32 UTC", in the person's own time zone. */
export function formatWhen(iso: string): string {
  const day = formatDay(iso)
  return day ? `${day}, ${formatClock(new Date(iso))}` : ''
}

/** A mailbox's state in words, as its row and its inbox say it. */
export function stateWords(mailbox: api.Mailbox): string {
  switch (mailbox.state) {
    case 'waiting':
      return 'Waiting for Gmail'
    case 'receiving':
      return mailbox.receiving_since
        ? `Receiving since ${formatDay(mailbox.receiving_since)}`
        : 'Receiving'
    case 'paused':
      return 'Paused by Permaily'
    default:
      return "Can't be read"
  }
}

/** The name a mailbox goes by: its label, or its address when it has none. */
export function mailboxName(mailbox: api.Mailbox): string {
  return mailbox.label || mailbox.address || 'Mailbox'
}

/** support@permaily.com, as a `mailto:` link: the app sends nothing itself. */
export function Support() {
  return <TextAnchor href={`mailto:${supportAddress}`}>{supportAddress}</TextAnchor>
}

/**
 * Copies a value the person needs elsewhere, such as an address or a code, and says whether it worked. Nothing
 * is kept: it goes to the clipboard because the person asked.
 */
export function CopyButton({ value, what }: { value: string; what: string }) {
  const [copied, setCopied] = useState<'idle' | 'copied' | 'failed'>('idle')
  async function copy() {
    try {
      await navigator.clipboard.writeText(value)
      setCopied('copied')
    } catch {
      setCopied('failed')
    }
  }
  return (
    <span className="inline-flex flex-wrap items-baseline gap-x-2">
      <TextAction size="sm" onClick={copy}>
        Copy<span className="sr-only"> {what}</span>
      </TextAction>
      <span role="status" className="text-small text-ink-2 empty:hidden">
        {copied === 'copied'
          ? 'Copied'
          : copied === 'failed'
            ? "Couldn't copy. Select it and copy it yourself."
            : ''}
      </span>
    </span>
  )
}

/** How something that has no rows yet stands: loading, or failed with a way to try again. */
export function LoadState({
  failure,
  what,
  onRetry,
}: {
  failure: string | null
  /** What could not load, such as "Your mailboxes". */
  what: string
  onRetry: () => void
}) {
  if (!failure) {
    return (
      <p role="status" className="border-b border-rule py-3 text-small text-ink-2">
        Loading
      </p>
    )
  }
  return (
    <div
      role="alert"
      className="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-rule py-3 text-small text-danger"
    >
      <p>
        {what} couldn't load. {failure}
      </p>
      <TextAction onClick={onRetry}>Try again</TextAction>
    </div>
  )
}
