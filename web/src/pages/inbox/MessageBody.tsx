import type * as api from '../../lib/api.ts'

/**
 * The body of a message: its text, always, as text. The sender wrote it, so it is never markup: React puts it in
 * the page as a text node, no address in it becomes a link, and a long line or a huge message wraps inside the
 * column instead of widening the page.
 *
 * Where ADR 0026's fifth pull request plugs in: it adds `MessageFrame.tsx` and makes this component choose
 * between the frame and `MessageText` by the person's choice of view, which the switch in the head sets (the
 * `views` slot of `MessageHead`). The column around this fills the rest of the viewport (`flex-1`), so the frame
 * can take that height. Until then `message.html` is absent and nothing here reads it.
 */
export function MessageBody({ message }: { message: api.Message }) {
  return <MessageText message={message} />
}

/** The Text view: the message's text as the enclave took it out of the message. */
export function MessageText({ message }: { message: api.Message }) {
  if (message.text === '') {
    return (
      <p className="text-body text-ink-2">
        {message.unparsed ? 'There is no text to show.' : 'This message has no text.'}
      </p>
    )
  }
  return (
    <div
      translate="no"
      dir="auto"
      className="max-w-prose whitespace-pre-wrap wrap-anywhere text-lead text-ink"
    >
      {message.text}
    </div>
  )
}
