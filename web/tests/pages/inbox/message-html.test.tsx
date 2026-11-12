import { act, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { frameLabel } from '../../../src/pages/inbox/MessageFrame.tsx'
import { current, expectNotKept, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { hostile, messageOf, personal, summaryOf } from '../../helpers/mailboxes.ts'
import { later, ok, refused, server } from '../../helpers/server.ts'

// ADR 0026, "Showing a message's HTML", tests 32 to 34: the formatted view, its frame and the link page, against
// a played server. The frame itself is tested in real browsers (tests/message-frame).

const one = messageOf()
const path = `/inbox/mbx_personal/${one.id}`
const messageRoute = `GET /api/v1/mailboxes/mbx_personal/messages/${one.id}`

function htmlOf(overrides: Record<string, unknown> = {}) {
  return {
    status: 'shown',
    remote_images: 0,
    remote_other: 0,
    inline_images: 0,
    inline_images_omitted: 0,
    inline_images_omitted_by: { too_large: 0, animated: 0, unsupported: 0 },
    links: 2,
    ...overrides,
  }
}

function showing(message: Record<string, unknown>) {
  server
    .signedIn()
    .on('GET /api/v1/mailboxes/mbx_personal', ok(personal))
    .on(messageRoute, ok(message))
}

function frame(): HTMLIFrameElement | null {
  return document.querySelector('iframe')
}

function pressed(name: string): string | null {
  return screen.getByRole('button', { name }).getAttribute('aria-pressed')
}

afterEach(() => {
  vi.restoreAllMocks()
})

describe('32: the switch, the notes, the labels', () => {
  it('opens formatted first, keeps the choice for the session, and the Text view says what it is', async () => {
    showing(messageOf({ html: htmlOf() }))
    const { user } = await open(path)
    await screen.findByRole('heading', { level: 1, name: 'Dinner on Friday' })
    expect(pressed('Formatted')).toBe('true')
    expect(pressed('Text')).toBe('false')
    expect(screen.getByText(frameLabel)).toBeTruthy()
    expect(frame()).not.toBeNull()
    // The sender as the enclave read it, above the label.
    expect(screen.getByText('Ana Ruiz')).toBeTruthy()
    await expectAccessible()

    await user.click(screen.getByRole('button', { name: 'Text' }))
    expect(pressed('Text')).toBe('true')
    expect(frame()).toBeNull()
    expect(screen.queryByText(frameLabel)).toBeNull()
    expect(
      screen.getByText(
        'This is the plain-text version the sender included. It may differ from the formatted one.',
      ),
    ).toBeTruthy()

    // The message opened again from the list opens in the view chosen; nothing of it is in storage.
    server
      .on('GET /api/v1/mailboxes', ok({ mailboxes: [personal], limit: 5 }))
      .on(
        'GET /api/v1/mailboxes/mbx_personal/messages?limit=50',
        ok({ messages: [summaryOf(1, { subject: 'Dinner on Friday' })] }),
      )
    await user.click(screen.getByRole('link', { name: 'Back to Personal' }))
    await user.click(await screen.findByRole('link', { name: /Dinner on Friday/ }))
    await screen.findByRole('heading', { level: 1, name: 'Dinner on Friday' })
    expect(pressed('Text')).toBe('true')
    expectNotKept('Formatted', 'formatted', 'text')
    await user.click(screen.getByRole('button', { name: 'Formatted' }))
    expect(frame()).not.toBeNull()
  })

  it('says the text of an HTML-only message is the formatted one without formatting, and shows no switch without HTML', async () => {
    showing(messageOf({ html: htmlOf(), text_from: 'html' }))
    const { user } = await open(path)
    await screen.findByRole('heading', { level: 1, name: 'Dinner on Friday' })
    await user.click(screen.getByRole('button', { name: 'Text' }))
    expect(
      screen.getByText('This is the text of the formatted message, without its formatting.'),
    ).toBeTruthy()

    showing(messageOf())
    await open(path)
    await screen.findByRole('heading', { level: 1, name: 'Dinner on Friday' })
    expect(screen.queryByRole('button', { name: 'Formatted' })).toBeNull()
    expect(frame()).toBeNull()
    expect(screen.queryByText(/plain-text version|without its formatting/)).toBeNull()
  })

  it('says what was not loaded and not shown, and why', async () => {
    await user32(
      htmlOf({
        remote_images: 3,
        inline_images_omitted: 4,
        inline_images_omitted_by: { too_large: 1, animated: 2, unsupported: 1 },
      }),
    )
    expect(screen.getByText('3 remote images were not loaded')).toBeTruthy()
    expect(
      screen.getByText(
        '4 images not shown: 1 too large, 2 animated, 1 not an image Permaily shows',
      ),
    ).toBeTruthy()
    // There is no way to load them.
    expect(screen.queryByRole('button', { name: /load/i })).toBeNull()

    await user32(
      htmlOf({
        remote_images: 1,
        inline_images_omitted: 1,
        inline_images_omitted_by: { too_large: 0, animated: 1, unsupported: 0 },
      }),
    )
    expect(screen.getByText('1 remote image was not loaded')).toBeTruthy()
    expect(screen.getByText('1 image not shown: 1 animated')).toBeTruthy()
  })

  it('shows the text, and says why, when the HTML is too large, unreadable or unavailable', async () => {
    await user32(htmlOf({ status: 'too_large' }))
    expect(screen.getByText(/formatting is too large to show here/)).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Formatted' })).toBeNull()
    expect(frame()).toBeNull()
    expect(screen.getByRole('region', { name: 'Message text' })).toBeTruthy()
    expect(screen.getByRole('link', { name: 'Download original' })).toBeTruthy()

    await user32(htmlOf({ status: 'unreadable' }))
    expect(screen.getByText(/couldn't read this message's formatting/)).toBeTruthy()

    const { user } = await user32(htmlOf({ status: 'unavailable' }))
    expect(
      screen.getByText("The formatted view isn't available right now. Try again in a moment."),
    ).toBeTruthy()
    server.on(messageRoute, ok(messageOf({ html: htmlOf() })))
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    await waitFor(() => expect(frame()).not.toBeNull())
  })

  it('draws the label and the sender in the app, whatever the message says', async () => {
    showing(
      messageOf({
        html: htmlOf(),
        subject: hostile,
        from: [{ name: hostile, address: 'eve@example.com' }],
      }),
    )
    await open(path)
    await screen.findByRole('heading', { level: 1, name: hostile })
    expect(screen.getByText(frameLabel).tagName).toBe('P')
    expect(document.querySelector('img')).toBeNull()
    expect(frame()?.getAttribute('title')).toBe(`Message: ${hostile}`)
  })
})

async function user32(html: Record<string, unknown>) {
  showing(messageOf({ html }))
  const opened = await open(path)
  await screen.findByRole('heading', { level: 1, name: 'Dinner on Friday' })
  return opened
}

describe('33: the frame', () => {
  it('has exactly its attributes, and is a new element for each message', async () => {
    showing(messageOf({ html: htmlOf() }))
    await open(path)
    await screen.findByRole('heading', { level: 1, name: 'Dinner on Friday' })
    const el = frame() as HTMLIFrameElement
    const attributes = Object.fromEntries([...el.attributes].map((a) => [a.name, a.value]))
    expect(attributes).toEqual({
      src: `/api/v1/mailboxes/mbx_personal/messages/${one.id}/html`,
      sandbox: 'allow-popups allow-popups-to-escape-sandbox',
      referrerpolicy: 'no-referrer',
      title: 'Message: Dinner on Friday',
    })

    const other = messageOf({ id: `${'0'.repeat(31)}2`, subject: '', html: htmlOf() })
    server.on(`GET /api/v1/mailboxes/mbx_personal/messages/${other.id}`, ok(other))
    await open(`/inbox/mbx_personal/${other.id}`)
    await screen.findByRole('heading', { level: 1, name: 'No subject' })
    const next = frame() as HTMLIFrameElement
    expect(next).not.toBe(el)
    expect(next.getAttribute('title')).toBe('Message: No subject')
  })

  it('hides a message the back/forward cache brought back until the session and the message answer again', async () => {
    showing(messageOf({ html: htmlOf() }))
    await open(path)
    await screen.findByRole('heading', { level: 1, name: 'Dinner on Friday' })
    const answer = later()
    server.on(messageRoute, answer.answer)
    act(() => {
      window.dispatchEvent(pageShow(true))
    })
    expect(frame()).toBeNull()
    expect(screen.queryByText('Are we still on', { exact: false })).toBeNull()
    expect(screen.getByText('Checking…')).toBeTruthy()
    await waitFor(() => expect(server.asked(messageRoute).length).toBe(2))
    answer.give(ok(messageOf({ html: htmlOf() })))
    await waitFor(() => expect(frame()).not.toBeNull())

    // A page shown again without the cache changes nothing.
    act(() => {
      window.dispatchEvent(pageShow(false))
    })
    expect(frame()).not.toBeNull()

    // Signed out meanwhile: the message never shows again.
    server.signedOut()
    act(() => {
      window.dispatchEvent(pageShow(true))
    })
    await waitFor(() => expect(current()).toBe('/sign-in'))
    expect(frame()).toBeNull()
  })

  it('says a message deleted meanwhile is not there any more', async () => {
    showing(messageOf({ html: htmlOf() }))
    await open(path)
    await screen.findByRole('heading', { level: 1, name: 'Dinner on Friday' })
    server.on(messageRoute, refused(404, 'not_found'))
    act(() => {
      window.dispatchEvent(pageShow(true))
    })
    await screen.findByRole('heading', { level: 1, name: "That message isn't there any more" })
  })
})

function pageShow(persisted: boolean): Event {
  const event = new Event('pageshow')
  Object.defineProperty(event, 'persisted', { value: persisted })
  return event
}

const linkPath = `/inbox/mbx_personal/${one.id}/links/3.AbCdEfGhIjKlMnOp`
const linkRoute = `GET /api/v1/mailboxes/mbx_personal/messages/${one.id}/links/3.AbCdEfGhIjKlMnOp`

describe('34: the link page', () => {
  it('shows where a link leads as text, and opens it only from its button', async () => {
    const opened = vi.spyOn(window, 'open').mockReturnValue(null)
    server.signedIn().on(
      linkRoute,
      ok({
        link: '3.AbCdEfGhIjKlMnOp',
        url: 'https://shop.example:8443/a?b=c#d',
        host: 'shop.example:8443',
      }),
    )
    const { user } = await open(linkPath)
    const heading = await screen.findByRole('heading', { level: 1, name: /This link leads to/ })
    expect(heading.textContent).toBe('This link leads toshop.example')
    expect(screen.getByText('Port', { exact: false }).textContent).toBe('Port 8443, https')
    expect(screen.getByText('https://shop.example:8443/a?b=c#d')).toBeTruthy()
    expect(screen.queryByText(/The message showed/)).toBeNull()
    expect(
      screen.getByText(
        (_, el) =>
          el?.tagName === 'P' &&
          el.textContent ===
            'Opening it tells shop.example:8443 that the link was followed, and when. The address itself may tell them who you are. Nothing is sent to shop.example:8443 until you press Open.',
      ),
    ).toBeTruthy()
    // The destination is in no link, no src, nothing a browser would fetch or preview.
    for (const el of document.querySelectorAll(
      '[href], [src], [srcset], [action], [formaction], [ping], [poster]',
    )) {
      for (const name of ['href', 'src', 'srcset', 'action', 'formaction', 'ping', 'poster']) {
        expect(el.getAttribute(name) ?? '').not.toContain('shop.example')
      }
    }
    expect(screen.getByRole('link', { name: 'Back to the message' }).getAttribute('href')).toBe(
      `/inbox/mbx_personal/${one.id}`,
    )
    await expectAccessible()
    expect(opened).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: 'Open shop.example:8443' }))
    expect(opened).toHaveBeenCalledTimes(1)
    expect(opened).toHaveBeenCalledWith(
      'https://shop.example:8443/a?b=c#d',
      '_blank',
      'noopener,noreferrer',
    )
    expect(screen.getByText('Opened in a new tab. You can close this one.')).toBeTruthy()
    expect(current()).toBe(linkPath)
    expectNotKept('shop.example')
  })

  it('says when the text named another host, and that http is not encrypted', async () => {
    server.signedIn().on(
      linkRoute,
      ok({
        link: '3.AbCdEfGhIjKlMnOp',
        url: 'http://xn--pypal-4ve.example/login',
        host: 'xn--pypal-4ve.example',
        shown: 'paypal.example',
      }),
    )
    await open(linkPath)
    await screen.findByRole('heading', { level: 1, name: /This link leads to/ })
    expect(screen.getByText('http, not encrypted')).toBeTruthy()
    expect(
      screen.getByText(
        (_, el) =>
          el?.textContent ===
          'The message showed paypal.example, but this link leads to xn--pypal-4ve.example.',
      ),
    ).toBeTruthy()
    await expectAccessible()
  })

  it('offers no button when the server and the browser disagree, or the link changed or is gone', async () => {
    const cases: Array<[unknown, string]> = [
      [
        ok({ link: '3.AbCdEfGhIjKlMnOp', url: 'https://a.example/', host: 'b.example' }),
        "This link can't be opened from Permaily.",
      ],
      [
        ok({ link: '3.AbCdEfGhIjKlMnOp', url: 'javascript:alert(1)', host: '' }),
        "This link can't be opened from Permaily.",
      ],
      [
        ok({ link: '3.AbCdEfGhIjKlMnOp', url: 'not a url', host: 'x' }),
        "This link can't be opened from Permaily.",
      ],
      [
        refused(409, 'link_changed'),
        'This link changed since the message was opened. Close this tab, open the message again, then the link.',
      ],
      [
        refused(404, 'not_found'),
        "This link isn't there any more. The message may have been deleted.",
      ],
      [
        refused(503, 'busy'),
        'Permaily is busy. Close this tab and open the link again in a moment.',
      ],
      [
        refused(500, 'internal'),
        "This link can't be shown right now. Close this tab and open it again from the message.",
      ],
    ]
    for (const [reply, words] of cases) {
      server.signedIn().on(linkRoute, reply as never)
      await open(linkPath)
      await screen.findByText(words)
      expect(screen.queryByRole('button', { name: /^Open/ })).toBeNull()
      expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('A link in a message')
    }
    await expectAccessible()
  })

  it('asks nothing for an address that is not a link token, and writes other characters as escapes', async () => {
    server.signedIn()
    await open(`/inbox/mbx_personal/${one.id}/links/0.short`)
    await screen.findByText("This link isn't there any more. The message may have been deleted.")

    const { printable } = await import('../../../src/pages/inbox/Link.tsx')
    expect(printable('a‮b c')).toBe('a\\u{202e}b\\u{20}c')
  })

  it('focuses the heading, then the address, the button and the way back in order', async () => {
    vi.spyOn(window, 'open').mockReturnValue(null)
    server
      .signedIn()
      .on(
        linkRoute,
        ok({ link: '3.AbCdEfGhIjKlMnOp', url: 'https://a.example/', host: 'a.example' }),
      )
    const { user } = await open(linkPath)
    const heading = await screen.findByRole('heading', { level: 1, name: /This link leads to/ })
    await waitFor(() => expect(document.activeElement).toBe(heading))
    await user.tab()
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Open a.example' }))
    await user.tab()
    expect(document.activeElement).toBe(screen.getByRole('link', { name: 'Back to the message' }))
    await user.keyboard('{Shift>}{Tab}{/Shift}')
    await user.keyboard('{Enter}')
    expect(window.open).toHaveBeenCalledTimes(1)
    await settle()
    within(document.body)
  })
})
