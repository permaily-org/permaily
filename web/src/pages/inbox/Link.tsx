import { useEffect, useRef, useState } from 'react'
import { useParams } from 'react-router'
import { Button, TextLink } from '../../components/ui/index.ts'
import * as api from '../../lib/api.ts'
import { useSession } from '../../lib/session.tsx'
import { cx, pagePadding } from '../../lib/styles.ts'

/** What the page says when it offers nothing to open. */
export const linkProblems = {
  link_changed:
    'This link changed since the message was opened. Close this tab, open the message again, then the link.',
  not_found: "This link isn't there any more. The message may have been deleted.",
  busy: 'Permaily is busy. Close this tab and open the link again in a moment.',
  refused: "This link can't be opened from Permaily.",
  other: "This link can't be shown right now. Close this tab and open it again from the message.",
} as const

type Found = { url: URL; shown?: string }

/**
 * Where a link of a message leads to (`url`), parsed by the browser's own `URL` parser: the server's address must
 * be http or https and its host the one the server said. The one `URL` object is what the page shows and where it
 * goes.
 */
export function parseDestination(answer: api.MessageLink): Found | null {
  let url: URL
  try {
    url = new URL(answer.url)
  } catch {
    return null
  }
  if ((url.protocol !== 'http:' && url.protocol !== 'https:') || url.host !== answer.host)
    return null
  return { url, ...(answer.shown ? { shown: answer.shown } : {}) }
}

/**
 * Every code point outside printable ASCII written as `\u{…}`: the server writes ASCII only, so this is a second
 * line against an address that would read as something else.
 */
export function printable(s: string): string {
  let out = ''
  for (const ch of s) {
    const c = ch.codePointAt(0) ?? 0
    out += c >= 0x21 && c <= 0x7e ? ch : `\\u{${c.toString(16)}}`
  }
  return out
}

/**
 * The page a link of a message opens, in a new tab, before anything is contacted (ADR 0026, "The link page"):
 * where the link leads, as text and never as a link, what following it tells that site, and a button. The
 * destination is in no `href`, `src` or prefetchable attribute, and not in the address bar, until the button is
 * pressed; a button is not a link, so a middle click, "Open in new tab", a long press or a preview does nothing.
 * The second place the app leaves for another site (web/AGENTS.md). Nothing of it is stored.
 */
export function LinkPage() {
  const { mailboxId = '', messageId = '', link = '' } = useParams()
  const { failed } = useSession()
  const [found, setFound] = useState<Found | null>(null)
  const [problem, setProblem] = useState<string | null>(null)
  const [opened, setOpened] = useState(false)
  const heading = useRef<HTMLHeadingElement>(null)

  useEffect(() => {
    let live = true
    api.messageLink(mailboxId, messageId, link).then(
      (answer) => {
        if (!live) return
        const destination = parseDestination(answer)
        if (destination) setFound(destination)
        else setProblem(linkProblems.refused)
      },
      (error) => {
        if (!live) return
        const code = failed(error)
        if (!code) return
        setProblem(
          code === 'link_changed' || code === 'not_found' || code === 'busy'
            ? linkProblems[code]
            : linkProblems.other,
        )
      },
    )
    return () => {
      live = false
    }
  }, [mailboxId, messageId, link, failed])

  // The page is opened in a tab of its own: focus starts on what it says, once it says it.
  const said = found !== null || problem !== null
  useEffect(() => {
    if (said) heading.current?.focus()
  }, [said])

  const back = `/inbox/${encodeURIComponent(mailboxId)}/${encodeURIComponent(messageId)}`
  const url = found?.url
  const host = url?.host ?? ''

  function open() {
    if (!url) return
    window.open(url.href, '_blank', 'noopener,noreferrer')
    setOpened(true)
  }

  return (
    <div className={cx(pagePadding, 'max-w-3xl')}>
      <h1 ref={heading} tabIndex={-1} className="text-heading font-medium focus:outline-none">
        {url ? (
          <>
            This link leads to
            <span className="mt-2 block font-mono text-title wrap-anywhere">
              <bdi dir="ltr">{printable(url.hostname)}</bdi>
            </span>
          </>
        ) : (
          'A link in a message'
        )}
      </h1>
      {url ? (
        <>
          <p className="mt-2 text-small text-ink-2">
            {url.port && (
              <>
                Port <span className="font-mono">{url.port}</span>,{' '}
              </>
            )}
            {url.protocol === 'https:' ? 'https' : 'http, not encrypted'}
          </p>
          <p className="mt-5 border-y border-rule py-3 font-mono text-mono break-all">
            <bdi dir="ltr">{printable(url.href)}</bdi>
          </p>
          {found?.shown && (
            <p className="mt-4 bg-attention-bg px-3 py-2 text-body text-attention">
              The message showed <bdi className="font-mono">{printable(found.shown)}</bdi>, but this
              link leads to <bdi className="font-mono">{printable(host)}</bdi>.
            </p>
          )}
          <p className="mt-4 text-body text-ink-2">
            Opening it tells <bdi className="font-mono">{printable(host)}</bdi> that the link was
            followed, and when. The address itself may tell them who you are. Nothing is sent to{' '}
            <bdi className="font-mono">{printable(host)}</bdi> until you press Open.
          </p>
          <div className="mt-6 flex flex-wrap items-center gap-4">
            <Button onClick={open}>
              Open <bdi dir="ltr">{printable(host)}</bdi>
            </Button>
            <TextLink to={back}>Back to the message</TextLink>
          </div>
          <p role="status" className="mt-4 text-body empty:hidden">
            {opened ? 'Opened in a new tab. You can close this one.' : ''}
          </p>
        </>
      ) : (
        <>
          <p role="status" className="mt-5 text-body text-ink-2">
            {problem ?? 'Looking up where it leads…'}
          </p>
          {problem && (
            <p className="mt-4">
              <TextLink to={back}>Back to the message</TextLink>
            </p>
          )}
        </>
      )}
    </div>
  )
}
