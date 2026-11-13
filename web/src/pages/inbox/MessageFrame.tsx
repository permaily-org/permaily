import * as api from '../../lib/api.ts'

/** The label the app draws on the frame's top edge, in its own type: the message can draw a copy, never change it. */
export const frameLabel =
  'Email content. Permaily never asks you to sign in or confirm anything inside a message.'

/**
 * The formatted view (ADR 0026, "The frame"): a sandboxed `<iframe>` whose document the enclave writes from the
 * message and serves with a policy of its own. It runs nothing, fetches nothing but the images that travel in the
 * message, and opens links only as a new tab on the link page. The element carries `src`, `sandbox`,
 * `referrerpolicy` and `title` and nothing else; a new element per message (the `key`), its `src` never changed.
 * The app never touches its document and never takes a size from it: it fills the rest of the column's height and
 * scrolls inside itself, a white sheet in a hairline border in both themes, so the label above always shows. Frame
 * and label are one left-aligned column of 760px at most: mail is written for about 600px, and its width is the
 * app's to set, outside the message, where the sender has no say.
 */
export function MessageFrame({ mailboxId, message }: { mailboxId: string; message: api.Message }) {
  return (
    <div className="flex min-h-96 w-full max-w-[760px] flex-1 flex-col">
      <p className="rounded-t-sm border border-b-0 border-rule bg-sunken px-3 py-1.5 text-small text-ink-2">
        {frameLabel}
      </p>
      <div className="flex flex-1 flex-col [&>iframe]:min-h-96 [&>iframe]:w-full [&>iframe]:flex-1 [&>iframe]:rounded-b-sm [&>iframe]:border [&>iframe]:border-rule [&>iframe]:bg-raised">
        <iframe
          key={message.id}
          src={api.messageHtmlUrl(mailboxId, message.id)}
          sandbox="allow-popups allow-popups-to-escape-sandbox"
          referrerPolicy="no-referrer"
          title={`Message: ${message.subject || 'No subject'}`}
        />
      </div>
    </div>
  )
}
