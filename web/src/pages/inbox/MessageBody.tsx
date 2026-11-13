import { useState } from 'react'
import type * as api from '../../lib/api.ts'
import { cx } from '../../lib/styles.ts'
import { MessageFrame } from './MessageFrame.tsx'

/** The two views of a message: its HTML in the frame, or its text. */
export type MessageView = 'formatted' | 'text'

/**
 * The view the person last chose, in the app's memory for the session: never in storage, so it is gone with the
 * tab. Formatted first.
 */
let chosen: MessageView = 'formatted'

/** Whether a message can be shown formatted: its HTML is there and the enclave rendered it. */
export function canFormat(message: api.Message): boolean {
  return message.html?.status === 'shown'
}

/** The view a message opens in, and how to change it (kept for the session). */
export function useMessageView(): [MessageView, (view: MessageView) => void] {
  const [view, setView] = useState<MessageView>(chosen)
  return [
    view,
    (next: MessageView) => {
      chosen = next
      setView(next)
    },
  ]
}

/** The switch between the views, two buttons with `aria-pressed`; only for a message that can be shown formatted. */
export function ViewSwitch({
  view,
  onChange,
}: {
  view: MessageView
  onChange: (view: MessageView) => void
}) {
  const option = (value: MessageView, label: string) => (
    <button
      type="button"
      aria-pressed={view === value}
      onClick={() => onChange(value)}
      className={cx(
        'h-8 px-3 text-small font-medium first:rounded-l-sm last:rounded-r-sm',
        view === value ? 'bg-ink text-paper' : 'text-ink-2 hover:text-ink',
      )}
    >
      {label}
    </button>
  )
  return (
    <fieldset
      aria-label="View"
      className="m-0 inline-flex min-w-0 rounded-sm border border-rule p-0"
    >
      {option('formatted', 'Formatted')}
      {option('text', 'Text')}
    </fieldset>
  )
}

/**
 * The body of a message: the frame in the formatted view, its text otherwise. The text is never markup: React puts
 * it in the page as a text node, no address in it becomes a link, and a long line wraps inside the column.
 */
export function MessageBody({
  mailboxId,
  message,
  view,
}: {
  mailboxId: string
  message: api.Message
  view: MessageView
}) {
  if (view === 'formatted' && canFormat(message)) {
    return <MessageFrame mailboxId={mailboxId} message={message} />
  }
  return <MessageText message={message} />
}

/** What the Text view says it shows: the sender writes the text part apart from the HTML. */
export function textNote(message: api.Message): string | null {
  if (message.text_from === 'html')
    return 'This is the text of the formatted message, without its formatting.'
  if (message.text_from === 'plain' && message.html) {
    return 'This is the plain-text version the sender included. It may differ from the formatted one.'
  }
  return null
}

/** The Text view: the message's text as the enclave took it out of the message, and what it is. */
export function MessageText({ message }: { message: api.Message }) {
  const note = textNote(message)
  return (
    <>
      {note && <p className="mb-4 max-w-prose text-small text-ink-2">{note}</p>}
      {message.text === '' ? (
        <p className="text-body text-ink-2">
          {message.unparsed ? 'There is no text to show.' : 'This message has no text.'}
        </p>
      ) : (
        <div
          translate="no"
          dir="auto"
          className="max-w-prose whitespace-pre-wrap wrap-anywhere text-lead text-ink"
        >
          {message.text}
        </div>
      )}
    </>
  )
}

/** "1 image" or "3 images". */
function images(n: number): string {
  return n === 1 ? '1 image' : `${n} images`
}

/**
 * What the app says of a message's HTML, under the switch: remote images never loaded, images not shown and why,
 * and why the text is shown instead of the formatted view. Each in the app's words, from counts the enclave made.
 */
export function htmlNotes(html: api.MessageHTML | undefined): string[] {
  if (!html) return []
  const notes: string[] = []
  switch (html.status) {
    case 'too_large':
      notes.push(
        "This message's formatting is too large to show here, so its text is shown. The original has all of it.",
      )
      break
    case 'unreadable':
      notes.push(
        "Permaily couldn't read this message's formatting, so its text is shown. The original has all of it.",
      )
      break
    case 'unavailable':
      notes.push("The formatted view isn't available right now. Try again in a moment.")
      break
  }
  if (html.status !== 'shown') return notes
  if (html.remote_images > 0) {
    notes.push(
      html.remote_images === 1
        ? '1 remote image was not loaded'
        : `${html.remote_images} remote images were not loaded`,
    )
  }
  const by = html.inline_images_omitted_by
  const why = [
    by.too_large > 0 && `${by.too_large} too large`,
    by.animated > 0 && `${by.animated} animated`,
    by.unsupported > 0 && `${by.unsupported} not an image Permaily shows`,
  ].filter(Boolean)
  if (html.inline_images_omitted > 0) {
    notes.push(`${images(html.inline_images_omitted)} not shown: ${why.join(', ')}`)
  }
  return notes
}
