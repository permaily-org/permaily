import { act, screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { MODEL_NOTICE_VERSION, modelNotice } from '../../../src/lib/model-notice.ts'
import reach from '../../formula/reach.json'
import { current, expectNotKept, named, open, settle, watchLeaving } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import {
  allowedRedirect,
  connectionOf,
  decisionRoute,
  deniedRedirect,
  previewOf,
  previewRoute,
  requestId,
  requestOf,
  sentence,
  signedInWithMailboxes,
  snapshotOf,
  startRoute,
  summary,
  vendorQuery,
} from '../../helpers/connections.ts'
import { personal, work } from '../../helpers/mailboxes.ts'
import {
  type Asked,
  ana,
  confirmationNeeded,
  later,
  meOf,
  network,
  ok,
  once,
  type Reply,
  refused,
  server,
} from '../../helpers/server.ts'

type User = Awaited<ReturnType<typeof open>>['user']

const authorize = `/oauth/authorize?${vendorQuery}`

/** The pieces of the vendor's request that must never be kept: its state, its challenge, its client and redirect. */
const secrets = [
  'st8te-secret',
  'Q0hBTExFTkdFLWNoYWxsZW5nZS1jaGFsbGVuZ2',
  'claude-code-client-metadata',
  '33418',
]

/** Opens the consent screen as Ana, the vendor's request answered with `request`, previews answered by `preview`. */
async function consent(
  request: Record<string, unknown> = requestOf(),
  preview: Parameters<typeof server.on>[1] = ok(previewOf()),
) {
  signedInWithMailboxes().on(startRoute, ok({ request })).on(previewRoute, preview)
  const opened = await open(authorize)
  await screen.findByRole('heading', { name: /asks to read your mail\.$/ })
  return opened
}

function allowButton() {
  return screen.getByRole('button', { name: named('Allow Claude Code') })
}

function denyButton() {
  return screen.getByRole('button', { name: named('Deny Claude Code') })
}

/** Confirms with the code of the message, in the dialog that asks for it. */
async function confirm(user: User) {
  const dialog = within(await screen.findByRole('dialog', { name: "Confirm it's you" }))
  await user.click(await dialog.findByRole('button', { name: 'Email me a code' }))
  await user.type(await dialog.findByLabelText('Code from the message'), '123456')
  await user.click(dialog.getByRole('button', { name: 'Confirm' }))
}

function confirmByMail() {
  server
    .on('GET /api/v1/auth/confirm', ok({ ways: ['email'] }))
    .on('POST /api/v1/auth/confirm/email/start', ok({ attempt: 'att_confirm', expires_in: 600 }))
    .on(
      'POST /api/v1/auth/confirm/email/finish',
      ok(meOf(ana, { confirmed_until: '2026-11-12T10:10:00Z' })),
    )
}

describe('V54: the hand-off', () => {
  it('takes the request out of the address bar before the router and the gate run, and hands it to the server', async () => {
    await consent()
    expect(current()).toBe('/oauth/authorize')
    expect(server.asked(startRoute)).toHaveLength(1)
    expect(server.asked(startRoute)[0].body).toEqual({ query: vendorQuery })
    // Held in memory: not in the router's state, which the history keeps, nor in storage.
    expectNotKept(...secrets, requestId)
    expect({ ...localStorage }).toEqual({})
    expect({ ...sessionStorage }).toEqual({})
  })

  it('finds nothing after a reload, and says to start again from the app', async () => {
    await consent()
    // A reload: the address is the cleaned one, and memory is gone.
    await open('/oauth/authorize')
    await screen.findByRole('heading', { name: "This request isn't here any more" })
    expect(server.asked(startRoute)).toHaveLength(1)
    await expectAccessible()
  })

  it('finds nothing when the browser restores the page from its cache', async () => {
    await consent()
    act(() => {
      window.dispatchEvent(new PageTransitionEvent('pageshow', { persisted: true }))
    })
    await screen.findByRole('heading', { name: "This request isn't here any more" })
  })

  it('finds nothing when the person comes back to it after leaving, as Back does', async () => {
    const leave = watchLeaving()
    const { user } = await consent()
    server.on(decisionRoute, ok({ redirect: deniedRedirect }))
    await user.click(denyButton())
    await waitFor(() => expect(leave).toHaveBeenCalledWith(deniedRedirect))
    await screen.findByRole('heading', { name: 'Going back to the app' })
    // Back loads the cleaned address again: nothing of the request is there.
    await open('/oauth/authorize')
    await screen.findByRole('heading', { name: "This request isn't here any more" })
  })

  it('keeps the request across signing in by the code of the message, in this tab', async () => {
    server
      .signedOut()
      .on('POST /api/v1/auth/email/start', ok({ attempt: 'att_1', expires_in: 600 }))
      .on('POST /api/v1/auth/email/finish', ok(meOf()))
      .on('GET /api/v1/mailboxes', ok({ mailboxes: [personal, work], limit: 5 }))
      .on(startRoute, ok({ request: requestOf() }))
      .on(previewRoute, ok(previewOf()))
    const { user } = await open(authorize)
    await user.type(await screen.findByLabelText('Email address'), 'ana@example.org')
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await user.type(await screen.findByLabelText('Code from the message'), '123456')
    expect(server.asked(startRoute)).toHaveLength(0)
    await user.click(screen.getByRole('button', { name: 'Sign in' }))
    await screen.findByRole('heading', { name: /asks to read your mail\.$/ })
    expect(current()).toBe('/oauth/authorize')
    expect(server.asked(startRoute)[0].body).toEqual({ query: vendorQuery })
    expectNotKept(...secrets, requestId)
  })

  it('sends the browser back to the vendor when the server refuses the request after proving its redirect', async () => {
    const leave = watchLeaving()
    signedInWithMailboxes().on(startRoute, ok({ redirect: deniedRedirect }))
    await open(authorize)
    await screen.findByRole('heading', { name: 'Going back to the app' })
    expect(leave).toHaveBeenCalledWith(deniedRedirect)
    expect(screen.getByText('localhost')).toBeTruthy()
  })

  it('never sends the browser to an address that is not a vendor’s', async () => {
    const leave = watchLeaving()
    signedInWithMailboxes().on(startRoute, ok({ redirect: 'javascript:alert(1)' }))
    await open(authorize)
    await screen.findByRole('heading', { name: "The request couldn't be read" })
    expect(leave).not.toHaveBeenCalled()
  })

  it('shows a client it cannot use, and never sends the browser anywhere', async () => {
    const leave = watchLeaving()
    signedInWithMailboxes().on(startRoute, refused(400, 'invalid_client'))
    await open(authorize)
    await screen.findByRole('heading', { name: "That request can't be used" })
    expect(screen.getByRole('alert').textContent).toBe(
      "That app's request can't be used. Start again from the app.",
    )
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull()
    expect(leave).not.toHaveBeenCalled()
    await expectAccessible()
  })

  it('tries again when the vendor’s details could not be read', async () => {
    signedInWithMailboxes().on(
      startRoute,
      once(refused(502, 'vendor_unreachable'), ok({ request: requestOf() })),
    )
    server.on(previewRoute, ok(previewOf()))
    const { user } = await open(authorize)
    await screen.findByText("That app's details couldn't be read. Try again in a few minutes.")
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    await screen.findByRole('heading', { name: /asks to read your mail\.$/ })
    expect(server.asked(startRoute)).toHaveLength(2)
  })

  it('says a request that ended is over, and forgets it', async () => {
    signedInWithMailboxes().on(startRoute, refused(409, 'request_ended'))
    await open(authorize)
    await screen.findByText('That request ended. Start again from the app.')
    expect(screen.queryByRole('button', { name: 'Try again' })).toBeNull()
  })

  it('forgets the request once the person goes elsewhere in the app, so Back finds nothing', async () => {
    const { user } = await consent()
    server
      .on(decisionRoute, refused(409, 'too_many_connections'))
      .on('GET /api/v1/connections', ok({ connections: [] }))
    await user.click(allowButton())
    // The way to make room says the request has to start again from the app.
    const alert = await screen.findByText(/Going there forgets this request/)
    expect(alert.textContent).toContain('start again from Claude Code')
    await user.click(screen.getByRole('link', { name: 'Your connections' }))
    await screen.findByRole('heading', { level: 1, name: 'No connections.' })
    // Back, within the same page: the router shows the consent screen again, with nothing held.
    act(() => window.history.back())
    await waitFor(() => expect(window.location.pathname).toBe('/oauth/authorize'))
    await screen.findByRole('heading', { name: "This request isn't here any more" })
    expect(server.asked(startRoute)).toHaveLength(1)
  })

  for (const alias of ['/oauth/authorize/', '/OAuth/Authorize', '/oauth/%61uthorize']) {
    it(`takes the request out of ${alias}, which the router reads as the same page, signed out`, async () => {
      server
        .signedOut()
        .on('POST /api/v1/auth/email/start', ok({ attempt: 'att_1', expires_in: 600 }))
        .on('POST /api/v1/auth/email/finish', ok(meOf()))
        .on('GET /api/v1/mailboxes', ok({ mailboxes: [personal, work], limit: 5 }))
        .on(startRoute, ok({ request: requestOf() }))
        .on(previewRoute, ok(previewOf()))
      const { user } = await open(`${alias}?${vendorQuery}`)
      await user.type(await screen.findByLabelText('Email address'), 'ana@example.org')
      // Where the sign-in screen goes next is in the history's state: nothing of the request is.
      expectNotKept(...secrets)
      await user.click(screen.getByRole('button', { name: 'Continue' }))
      await user.type(await screen.findByLabelText('Code from the message'), '123456')
      await user.click(screen.getByRole('button', { name: 'Sign in' }))
      await screen.findByRole('heading', { name: /asks to read your mail\.$/ })
      expect(server.asked(startRoute)[0].body).toEqual({ query: vendorQuery })
      expectNotKept(...secrets, requestId)
    })

    it(`takes the request out of ${alias} signed in, and shows it`, async () => {
      signedInWithMailboxes()
        .on(startRoute, ok({ request: requestOf() }))
        .on(previewRoute, ok(previewOf()))
      await open(`${alias}?${vendorQuery}#frag`)
      await screen.findByRole('heading', { name: /asks to read your mail\.$/ })
      expect(window.location.search).toBe('')
      expect(window.location.hash).toBe('')
      expect(server.asked(startRoute)[0].body).toEqual({ query: vendorQuery })
      expectNotKept(...secrets, requestId)
    })
  }

  it('says nothing more when the session ended meanwhile: the gate leads to the sign-in screen', async () => {
    signedInWithMailboxes().on(startRoute, refused(401, 'unauthorized'))
    server.on('GET /api/v1/auth/providers', ok({ providers: [] }))
    await open(authorize)
    server.on('GET /api/v1/me', refused(401, 'unauthorized'))
    await screen.findByLabelText('Email address')
  })
})

describe('V54: every section of the consent screen', () => {
  it('a verified vendor whose client runs on the person’s computer, asking without a query', async () => {
    await consent()
    const main = screen.getByRole('main')
    expect(main.textContent).toContain('claude.ai')
    expect(screen.getByText('Anthropic')).toBeTruthy()
    expect(main.textContent).toContain('Calls itself Claude Code')
    // Where the browser goes, and that any program on the computer can be this client.
    expect(screen.getByText('localhost')).toBeTruthy()
    expect(screen.getByText('This app runs on your computer.')).toBeTruthy()
    expect(main.textContent).toContain('Any program on your computer can say it is Claude Code.')
    // What it asks for: all mail of the mailboxes the person picks, new mail only to start with.
    expect(screen.getByText('All mail')).toBeTruthy()
    // Named by their labels once the mailboxes are read.
    expect(await screen.findByText('Personal')).toBeTruthy()
    const boxes = within(screen.getByRole('group', { name: 'Mailboxes' })).getAllByRole('checkbox')
    expect(boxes.map((box) => (box as HTMLInputElement).checked)).toEqual([true, true])
    expect(screen.getByText('Work')).toBeTruthy()
    expect(screen.getByRole('button', { name: 'Change mail since' })).toBeTruthy()
    expect(main.textContent).toContain('Who sent it and when, the subject and the text')
    expect(main.textContent).toContain('MCP, for agents')
    expect(main.textContent).toContain('14 Feb 2027, on its own')
    // What never leaves, and that the vendor isn't told.
    expect(screen.getByText('Sign-in codes and password resets')).toBeTruthy()
    expect(main.textContent).toContain("claude.ai isn't told what they hold back")
    // The model's notice, and that nothing leaves while the model can't be asked.
    expect(screen.getByRole('heading', { name: 'Before a model decides' })).toBeTruthy()
    for (const paragraph of modelNotice) expect(main.textContent).toContain(paragraph)
    expect(main.textContent).toContain(
      "If Permaily can't ask its model (no key, or a spend limit reached), this connection gets no mail until it can.",
    )
    // New mail only counts nothing yet, and says so.
    expect(screen.getByRole('region', { name: 'Preview' }).textContent).toContain(
      'New mail only: it reads mail that arrives from when you allow.',
    )
    // Allow and Deny, of the same weight, and focus on neither.
    expect(allowButton().className).toBe(denyButton().className)
    await settle()
    expect(document.activeElement).not.toBe(allowButton())
    expect(document.activeElement).not.toBe(denyButton())
    await expectAccessible()
  })

  it('a vendor nobody verified, asking with a query, sending the browser to its own site', async () => {
    await consent(
      requestOf({
        vendor: { domain: 'claude-ai.example' },
        client_name: 'Claude',
        redirect_host: 'claude-ai.example',
        loopback: false,
        resource: 'api',
        parts: ['sender', 'subject'],
        query: {
          formula: { v: 1, all: [] },
          id: 'qry_x',
          sentence: {
            lang: 'en',
            text: 'Mail about invoices',
            lead: [],
            lines: [
              { parts: [{ text: 'Mail about ' }, { text: 'invoices', value: true, model: true }] },
            ],
          },
          model: true,
        },
        mailboxes: [personal.id],
        draft: { mailboxes: [personal.id], since: { new: true } },
      }),
    )
    const main = screen.getByRole('main')
    expect(screen.getByText('Not verified by Permaily')).toBeTruthy()
    expect(screen.queryByText(/^Verified/)).toBeNull()
    expect(main.textContent).toContain('Calls itself Claude')
    expect(screen.queryByText('This app runs on your computer.')).toBeNull()
    expect(main.textContent).toContain('Mail about invoices(a model decides)')
    // The person accepts the query whole: no picker of mailboxes, no choice of mail since it did not ask a date.
    expect(screen.queryByRole('group', { name: 'Mailboxes' })).toBeNull()
    expect(screen.queryByRole('checkbox')).toBeNull()
    expect(screen.queryByRole('button', { name: 'Change mail since' })).toBeNull()
    expect(main.textContent).toContain('The API, for apps')
    expect(main.textContent).toContain('Who sent it and when and the subject')
    await expectAccessible()
  })

  it('a vendor’s name and values are text, isolated, never markup', async () => {
    await consent(requestOf({ client_name: '<img src=x onerror=alert(1)>‮evil' }))
    expect(document.querySelector('img')).toBeNull()
    const named = screen.getAllByText('<img src=x onerror=alert(1)>‮evil')
    for (const element of named) expect(element.tagName).toBe('BDI')
  })
})

describe('V54: the draft and its preview', () => {
  it('asks a preview of every draft from the newest, shows what approving it does, and allows exactly that draft', async () => {
    const leave = watchLeaving()
    confirmByMail()
    const pages = (asked: Asked) => {
      const body = asked.body as {
        draft: { mailboxes: string[]; since: { new: boolean } }
        mailbox: string
      }
      if (body.draft.since.new) return ok(previewOf([], { change: 'add_device' }))
      const one = body.draft.mailboxes.length === 1
      return ok(
        previewOf([summary(one ? 3 : 1), summary(2)], {
          change: one ? 'add_device' : 'replace',
          draft_id: one ? 'drf_one' : 'drf_two',
        }),
      )
    }
    const { user } = await consent(
      requestOf({
        connection: connectionOf(),
        change: 'add_device',
        snapshot: snapshotOf({ change: 'add_device', connection: 'con_claudecode', version: 7 }),
      }),
      pages,
    )
    expect(screen.getByText('This adds another device to your connection.')).toBeTruthy()
    expect(screen.getByRole('main').textContent).toContain('It is on 2 of 16 devices.')

    // From when: the last 30 days. The preview is asked again, from the newest, for the whole draft.
    await user.click(screen.getByRole('button', { name: 'Change mail since' }))
    const since = within(screen.getByRole('dialog', { name: 'From when Claude Code can read' }))
    await user.click(since.getByRole('radio', { name: /The last 30 days/ }))
    await user.click(since.getByRole('button', { name: 'Save' }))
    await screen.findByText('This replaces your connection.')
    const last = server.asked(previewRoute).at(-1)?.body as Record<string, unknown>
    expect(last.after).toBeUndefined()
    expect((last.draft as { mailboxes: string[] }).mailboxes).toEqual([personal.id, work.id])
    expect((last.draft as { since: { new: boolean; at: string } }).since.new).toBe(false)
    expect(screen.getByRole('region', { name: 'Preview' }).textContent).toContain('4emails')

    // One mailbox only: asked again, and it now adds a device.
    await user.click(await screen.findByRole('checkbox', { name: 'Work' }))
    await screen.findByText('This adds another device to your connection.')
    const narrowed = server.asked(previewRoute).at(-1)?.body as { draft: { mailboxes: string[] } }
    expect(narrowed.draft.mailboxes).toEqual([personal.id])
    await screen.findByText('Subject 3')
    await expectAccessible()

    // Allow sends that draft, the notice shown and the snapshot of that draft, after a fresh proof.
    server.on(decisionRoute, once(confirmationNeeded, ok({ redirect: allowedRedirect })))
    await user.click(allowButton())
    await confirm(user)
    await waitFor(() => expect(leave).toHaveBeenCalledWith(allowedRedirect))
    const decisions = server.asked(decisionRoute)
    expect(decisions).toHaveLength(2)
    const decision = decisions[1].body as Record<string, unknown>
    expect(decision.allow).toBe(true)
    expect(decision.model_notice).toBe(MODEL_NOTICE_VERSION)
    expect(decision.draft).toEqual(narrowed.draft)
    expect(decision.snapshot).toEqual(snapshotOf({ change: 'add_device', draft_id: 'drf_one' }))
    expectNotKept(...secrets, requestId, allowedRedirect, 'pmc_c0deOfTheTest')
  })

  it('counts by pages over each mailbox, at least that many while there is more, and counts on when asked', async () => {
    const pages = (asked: Asked) => {
      const body = asked.body as { mailbox: string; after?: string; draft_id?: string }
      if (body.mailbox === personal.id && !body.after)
        return ok(previewOf([summary(1)], { next: 'p2', draft_id: 'drf_all' }))
      if (body.mailbox === personal.id) return ok(previewOf([summary(2)], { draft_id: 'drf_all' }))
      if (!body.after) return ok(previewOf([summary(3)], { next: 'w2', draft_id: 'drf_all' }))
      if (body.after === 'w2')
        return ok(previewOf([summary(4)], { next: 'w3', draft_id: 'drf_all' }))
      return ok(previewOf([summary(5), summary(6)], { draft_id: 'drf_all' }))
    }
    const { user } = await consent(
      requestOf({ draft: { mailboxes: [personal.id, work.id], since: { new: false } } }),
      pages,
    )
    const preview = screen.getByRole('region', { name: 'Preview' })
    await waitFor(() => expect(preview.textContent).toContain('At least4emails'))
    // A next page carries the draft's identifier the page before answered.
    const second = server.asked(previewRoute)[1].body as Record<string, unknown>
    expect(second).toMatchObject({ mailbox: personal.id, after: 'p2', draft_id: 'drf_all' })
    expect(server.asked(previewRoute)).toHaveLength(4)
    await user.click(screen.getByRole('button', { name: 'Count more' }))
    await waitFor(() => expect(preview.textContent).toContain('6emails'))
    expect(preview.textContent).not.toContain('At least')
    expect(screen.queryByRole('button', { name: 'Count more' })).toBeNull()
  })

  it('does not allow a draft without a mailbox, and asks no preview of it', async () => {
    const { user } = await consent()
    await user.click(await screen.findByRole('checkbox', { name: 'Personal' }))
    await waitFor(() => expect(server.asked(previewRoute)).toHaveLength(2))
    await user.click(screen.getByRole('checkbox', { name: 'Work' }))
    expect(screen.getByText('Choose at least one mailbox.')).toBeTruthy()
    expect(allowButton().getAttribute('aria-disabled')).toBe('true')
    await user.click(allowButton())
    await settle()
    expect(server.asked(decisionRoute)).toHaveLength(0)
    expect(server.asked(previewRoute)).toHaveLength(2)
  })

  it('previews with the model only when asked, agreeing to the notice first, and says what the rules hold back', async () => {
    const pages = (asked: Asked) => {
      const body = asked.body as { ask_model?: boolean }
      if (!body.ask_model) return ok(previewOf([summary(1)], { needs_model: ['m2', 'm3'] }))
      return ok(previewOf([summary(1), summary(2)], { held: ['m3'], model: 'asked' }))
    }
    const { user } = await consent(
      requestOf({ draft: { mailboxes: [personal.id], since: { new: false } } }),
      pages,
    )
    await screen.findByText('2 more only a model can decide, and your rules may hold some back.')
    // The first time the model is asked, the account has not agreed to the notice.
    let first = true
    server.on(previewRoute, (asked) => {
      const body = asked.body as { ask_model?: boolean }
      if (body.ask_model && first) {
        first = false
        return refused(400, 'model_notice_needed')
      }
      return pages(asked)
    })
    server.on(
      'POST /api/v1/account/model-notice',
      ok({ ...ana, model_notice: MODEL_NOTICE_VERSION }),
    )
    await user.click(screen.getByRole('button', { name: 'Preview with the model' }))
    await user.click(
      await screen.findByRole('button', { name: 'Agree and preview with the model' }),
    )
    await screen.findByText(/1 held back by your rules/)
    expect(server.asked('POST /api/v1/account/model-notice')[0].body).toEqual({
      version: MODEL_NOTICE_VERSION,
    })
    expect(server.asked(previewRoute).at(-1)?.body).toMatchObject({ ask_model: true })
    expect(screen.getByRole('region', { name: 'Preview' }).textContent).toContain('2emails')
    // Agreeing to the notice for a preview allows nothing.
    expect(server.asked(decisionRoute)).toHaveLength(0)
    await expectAccessible()
  })

  it('says when a preview fails, and tries again', async () => {
    const { user } = await consent(
      requestOf({ draft: { mailboxes: [personal.id], since: { new: false } } }),
      once(network, ok(previewOf([summary(1)]))),
    )
    await screen.findByText(/The preview couldn't run\./)
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    await screen.findByText('Subject 1')
  })

  it('drops the answer of a draft that is no longer the one on the screen', async () => {
    const slow = later()
    const held = slow.answer as (asked: Asked) => Promise<Reply>
    const { user } = await consent(
      requestOf({ draft: { mailboxes: [personal.id, work.id], since: { new: false } } }),
      (asked) => {
        const body = asked.body as { draft: { mailboxes: string[] } }
        return body.draft.mailboxes.length === 2 ? held(asked) : ok(previewOf([summary(7)]))
      },
    )
    await user.click(await screen.findByRole('checkbox', { name: 'Work' }))
    await screen.findByText('Subject 7')
    slow.give(ok(previewOf([summary(8), summary(9)])))
    await settle()
    expect(screen.queryByText('Subject 8')).toBeNull()
    // The count of the draft on the screen stays as it was.
    expect(screen.getByText('Subject 7')).toBeTruthy()
  })
})

describe('V54: deciding', () => {
  it('denies without a fresh proof, and leaves only for the server’s address', async () => {
    const leave = watchLeaving()
    const { user } = await consent()
    server.on(decisionRoute, ok({ redirect: deniedRedirect }))
    await user.click(denyButton())
    await waitFor(() => expect(leave).toHaveBeenCalledWith(deniedRedirect))
    expect(server.asked(decisionRoute)[0].body).toEqual({ allow: false })
    expectNotKept(...secrets, requestId, deniedRedirect)
  })

  it('loads the request again when the model’s notice changed, and says so', async () => {
    const { user } = await consent()
    server.on(decisionRoute, refused(409, 'model_notice_changed'))
    await user.click(allowButton())
    await screen.findByText('This changed since you last read it')
    expect(
      screen.getByText(
        'What Permaily sends to its model changed since this page opened. Read it again, then decide.',
      ),
    ).toBeTruthy()
    expect(server.asked(startRoute)).toHaveLength(2)
    expect(server.asked(startRoute)[1].body).toEqual({ query: vendorQuery })
    await expectAccessible()
  })

  it('loads the request again when what allowing does changed, and commits nothing', async () => {
    const { user } = await consent()
    server.on(decisionRoute, refused(409, 'consent_changed'))
    await user.click(allowButton())
    await screen.findByText(
      'What allowing does changed since this page opened. Read it again, then decide.',
    )
    expect(server.asked(startRoute)).toHaveLength(2)
  })

  it('does nothing when the person does not confirm it is them', async () => {
    const leave = watchLeaving()
    confirmByMail()
    const { user } = await consent()
    server.on(decisionRoute, confirmationNeeded)
    await user.click(allowButton())
    const dialog = within(await screen.findByRole('dialog', { name: "Confirm it's you" }))
    await user.click(dialog.getByRole('button', { name: 'Cancel' }))
    await settle()
    expect(leave).not.toHaveBeenCalled()
    expect(screen.queryByRole('alert')?.textContent ?? '').toBe('')
    expect(allowButton().getAttribute('aria-disabled')).toBe('false')
  })

  it('leads to the connection when it is on 16 devices, and to the list when the account has as many as it can', async () => {
    const { user } = await consent(
      requestOf({
        connection: connectionOf(),
        change: 'add_device',
        snapshot: snapshotOf({ change: 'add_device' }),
      }),
    )
    server.on(decisionRoute, refused(409, 'too_many_devices'))
    await user.click(allowButton())
    const link = await screen.findByRole('link', { name: 'Open the connection' })
    expect(link.getAttribute('href')).toBe('/connections/claude.ai/con_claudecode')
    server.on(decisionRoute, refused(409, 'too_many_connections'))
    await user.click(allowButton())
    const list = await screen.findByRole('link', { name: 'Your connections' })
    expect(list.getAttribute('href')).toBe('/connections')
  })

  it('says a request that ended while it was shown is over', async () => {
    const { user } = await consent()
    server.on(decisionRoute, refused(409, 'request_ended'))
    await user.click(denyButton())
    await screen.findByRole('heading', { name: 'That request ended' })
  })

  it('does not allow under a notice the app does not show', async () => {
    await consent(requestOf({ model_notice: '2099-01-01' }))
    expect(allowButton().getAttribute('aria-disabled')).toBe('true')
    expect(
      screen.getByText(
        'This page is older than Permaily. Reload it from the app to read what applies now.',
      ),
    ).toBeTruthy()
  })

  it('neither previews with the model nor agrees to a notice the app does not show', async () => {
    const newer = '2099-01-01'
    signedInWithMailboxes()
      .on(
        startRoute,
        ok({
          request: requestOf({
            model_notice: newer,
            draft: { mailboxes: [personal.id], since: { new: false } },
          }),
        }),
      )
      .on(previewRoute, (asked) =>
        (asked.body as { ask_model?: boolean }).ask_model
          ? refused(400, 'model_notice_needed')
          : ok(previewOf([summary(1)])),
      )
      .on('POST /api/v1/account/model-notice', ok({ ...ana, model_notice: newer }))
    await open(authorize)
    await screen.findByRole('heading', { name: /asks to read your mail\.$/ })
    await screen.findByText('Subject 1')
    expect(allowButton().getAttribute('aria-disabled')).toBe('true')
    // The preview with the model is refused as Allow is: the page shows another notice than the one in force.
    expect(screen.queryByRole('button', { name: 'Preview with the model' })).toBeNull()
    expect(screen.getByRole('region', { name: 'Preview' }).textContent).toContain(
      'Reload it from the app to read what applies now.',
    )
    await settle()
    expect(server.asked('POST /api/v1/account/model-notice')).toHaveLength(0)
    expect(
      server.asked(previewRoute).some((a) => (a.body as { ask_model?: boolean }).ask_model),
    ).toBe(false)
  })

  it('agrees only to the version of the notice the page shows', async () => {
    let agreed = false
    const { user } = await consent(
      requestOf({ draft: { mailboxes: [personal.id], since: { new: false } } }),
      (asked) =>
        (asked.body as { ask_model?: boolean }).ask_model && !agreed
          ? refused(400, 'model_notice_needed')
          : ok(previewOf([summary(1)])),
    )
    server.on('POST /api/v1/account/model-notice', () => {
      agreed = true
      return ok(ana)
    })
    await user.click(await screen.findByRole('button', { name: 'Preview with the model' }))
    await user.click(
      await screen.findByRole('button', { name: 'Agree and preview with the model' }),
    )
    await waitFor(() => expect(agreed).toBe(true))
    expect(server.asked('POST /api/v1/account/model-notice').map((a) => a.body)).toEqual([
      { version: MODEL_NOTICE_VERSION },
    ])
  })

  it('says what failed and lets the person decide again', async () => {
    const { user } = await consent()
    server.on(decisionRoute, network)
    await user.click(denyButton())
    await screen.findByText("Permaily can't be reached. Check your connection, then try again.")
    expect(denyButton().getAttribute('aria-disabled')).toBe('false')
  })
})

describe('V54: the screen’s sentences', () => {
  it('reads a sentence of several lines as a list', async () => {
    await consent(
      requestOf({
        rules: [
          {
            id: 'rul_default',
            version: 1,
            sentence: [
              {
                lang: 'en',
                text: 'Mail that is either',
                lead: [{ text: 'Mail that is either' }],
                lines: [
                  { parts: [{ text: 'a sign-in code' }] },
                  { parts: [{ text: 'a password reset' }] },
                ],
              },
            ],
          },
        ],
      }),
    )
    const items = screen.getAllByRole('listitem').map((item) => item.textContent)
    expect(items).toContain('a sign-in code')
    expect(items).toContain('a password reset')
  })
})

describe('V54: mail since', () => {
  async function choose(user: User, name: RegExp) {
    await user.click(screen.getByRole('button', { name: 'Change mail since' }))
    const dialog = within(await screen.findByRole('dialog', { name: /^From when .* can read$/ }))
    await user.click(dialog.getByRole('radio', { name }))
    await user.click(dialog.getByRole('button', { name: 'Save' }))
  }

  function lastSince() {
    const last = server.asked(previewRoute).at(-1)
    return (last?.body as { draft: { since: unknown } } | undefined)?.draft.since
  }

  it('a request with a date may only be left at new mail only', async () => {
    const { user } = await consent(
      requestOf({
        query: {
          formula: { v: 1, all: [] },
          id: 'qry_x',
          sentence: {
            lang: 'en',
            text: 'Bookings',
            lead: [],
            lines: [{ parts: [{ text: 'Bookings' }] }],
          },
          model: false,
        },
        since: { new: false, at: '2026-10-01T00:00:00Z' },
        mailboxes: [personal.id],
        draft: { mailboxes: [personal.id], since: { new: false, at: '2026-10-01T00:00:00Z' } },
      }),
      ok(previewOf([summary(1)])),
    )
    expect(screen.getByRole('main').textContent).toContain('Mail since 1 Oct 2026')
    await user.click(screen.getByRole('button', { name: 'Change mail since' }))
    const dialog = within(
      await screen.findByRole('dialog', { name: 'From when Claude Code can read' }),
    )
    expect(dialog.getAllByRole('radio')).toHaveLength(2)
    await user.click(dialog.getByRole('radio', { name: /New mail only/ }))
    await user.click(dialog.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(lastSince()).toEqual({ new: true }))
    await choose(user, /What the app asked for/)
    await waitFor(() => expect(lastSince()).toEqual({ new: false, at: '2026-10-01T00:00:00Z' }))
  })

  it('a request without a query starts from the connection’s own, and offers the rest', async () => {
    const { user } = await consent(
      requestOf({
        connection: connectionOf(),
        draft: { mailboxes: [personal.id], since: { new: false, at: '2026-08-01T00:00:00Z' } },
      }),
      ok(previewOf([summary(1)])),
    )
    expect(screen.getByRole('main').textContent).toContain('Mail since 1 Aug 2026')
    await choose(user, /The last year/)
    await waitFor(() => {
      const since = lastSince() as { new: boolean; at: string }
      expect(since.new).toBe(false)
      expect(Date.now() - Date.parse(since.at)).toBeGreaterThan(364 * 86_400_000)
    })
    await choose(user, /All the mail Permaily keeps/)
    await waitFor(() => expect(lastSince()).toEqual({ new: false }))
    await choose(user, /What your connection reads now/)
    await waitFor(() => expect(lastSince()).toEqual({ new: false, at: '2026-08-01T00:00:00Z' }))
  })
})

describe('V54: what the model preview says', () => {
  it('says when the model could not be asked, or a spend limit held some back', async () => {
    let model = 'off'
    const { user } = await consent(
      requestOf({ draft: { mailboxes: [personal.id], since: { new: false } } }),
      (asked) =>
        ok(
          (asked.body as { ask_model?: boolean }).ask_model
            ? previewOf([summary(1)], { model })
            : previewOf([summary(1)]),
        ),
    )
    await user.click(await screen.findByRole('button', { name: 'Preview with the model' }))
    await screen.findByText("Permaily can't ask its model now. Nothing it would decide is counted.")
    model = 'limit'
    await user.click(screen.getByRole('checkbox', { name: 'Personal' }))
    await user.click(screen.getByRole('checkbox', { name: 'Personal' }))
    await user.click(await screen.findByRole('button', { name: 'Preview with the model' }))
    await screen.findByText("A spend limit held some back from the model. They aren't counted.")
  })

  it('says when agreeing to the notice failed', async () => {
    const { user } = await consent(
      requestOf({ draft: { mailboxes: [personal.id], since: { new: false } } }),
      (asked) =>
        (asked.body as { ask_model?: boolean }).ask_model
          ? refused(400, 'model_notice_needed')
          : ok(previewOf([summary(1)])),
    )
    server.on('POST /api/v1/account/model-notice', network)
    await user.click(await screen.findByRole('button', { name: 'Preview with the model' }))
    await user.click(
      await screen.findByRole('button', { name: 'Agree and preview with the model' }),
    )
    await screen.findByText("Permaily can't be reached. Check your connection, then try again.")
  })
})

describe('V54: what a replacement reaches that the connection did not', () => {
  /** A message of the preview, received at `at`. */
  function received(n: number, at: string) {
    return { ...summary(n), received_at: at }
  }

  /** The rows of the preview, each with whether it is marked new. */
  function rows() {
    const list = screen.getByRole('list', { name: 'Emails it would read' })
    return within(list)
      .getAllByRole('listitem')
      .map((row) => [
        row.textContent?.match(/Subject \d+/)?.[0],
        within(row).queryByText('new', { exact: true }) !== null,
      ])
  }

  function replacing(connection: Record<string, unknown>, overrides: Record<string, unknown> = {}) {
    return requestOf({
      connection: connectionOf(connection),
      draft: { mailboxes: [personal.id, work.id], since: { new: false } },
      change: 'replace',
      snapshot: snapshotOf({ change: 'replace', connection: 'con_claudecode', version: 7 }),
      ...overrides,
    })
  }

  /** Previews that answer each mailbox with its own messages, all replacing `connection` at `version`. */
  function byMailbox(
    pages: Record<string, unknown[]>,
    replaced: { connection: string; version: number } = {
      connection: 'con_claudecode',
      version: 7,
    },
  ) {
    return (asked: Asked) => {
      const { mailbox } = asked.body as { mailbox: string }
      const answer = previewOf(pages[mailbox] ?? [], { change: 'replace', draft_id: 'drf_wider' })
      return ok({ ...answer, snapshot: { ...answer.snapshot, ...replaced } })
    }
  }

  /**
   * The matches route as the server parses a formula (internal/query, checkTime): a time is RFC 3339 with seconds
   * and an offset and no fraction, or the whole formula is refused as `invalid_formula`.
   */
  function matchesAs(reply: Reply) {
    const time = /^\d{4}-\d{2}-\d{2}[Tt]\d{2}:\d{2}:\d{2}([Zz]|[+-]\d{2}:\d{2})$/
    return (asked: Asked) => {
      const { formula } = asked.body as {
        formula: { all: { any: { var: string; value: string }[] }[] }
      }
      const times = formula.all.flatMap((g) => g.any.filter((c) => c.var === 'received'))
      return times.every((c) => time.test(c.value)) ? reply : refused(400, 'invalid_formula')
    }
  }

  it('marks the mail of a mailbox it did not read, and mail older than it read, as new', async () => {
    await consent(
      replacing({
        mailboxes: [personal.id],
        since: { new: false, at: '2026-11-01T00:00:00Z' },
      }),
      byMailbox({
        [personal.id]: [received(1, '2026-11-10T09:00:00Z'), received(2, '2026-10-15T09:00:00Z')],
        [work.id]: [received(3, '2026-11-12T09:00:00Z')],
      }),
    )
    await screen.findByText('Subject 3')
    expect(rows()).toEqual([
      ['Subject 1', false],
      ['Subject 2', true],
      ['Subject 3', true],
    ])
    const preview = screen.getByRole('region', { name: 'Preview' })
    expect(preview.textContent).toContain(
      "2 of them your connection doesn't read. They're marked new.",
    )
    // Nothing of a query to compare: the old connection had none, so no other page was asked.
    expect(server.asked(`POST /api/v1/mailboxes/${personal.id}/matches`)).toHaveLength(0)
    await expectAccessible()
  })

  it('marks mail that arrived before a connection of new mail only as new', async () => {
    await consent(
      replacing({
        mailboxes: [personal.id, work.id],
        since: { new: true, at: '2026-11-01T10:00:00Z' },
      }),
      byMailbox({
        [personal.id]: [received(1, '2026-11-10T09:00:00Z'), received(2, '2026-11-01T09:59:59Z')],
      }),
    )
    await screen.findByText('Subject 2')
    expect(rows()).toEqual([
      ['Subject 1', false],
      ['Subject 2', true],
    ])
  })

  it('says the parts it would get that the connection does not, and another resource', async () => {
    await consent(
      replacing({ parts: ['sender', 'subject'], resource: 'api' }),
      byMailbox({ [personal.id]: [received(1, '2026-11-10T09:00:00Z')] }),
    )
    await screen.findByText('Subject 1')
    expect(rows()).toEqual([['Subject 1', false]])
    const preview = screen.getByRole('region', { name: 'Preview' })
    expect(preview.textContent).toContain(
      "It would also get the text of every one, which your connection doesn't.",
    )
    expect(preview.textContent).toContain(
      'It reads through MCP, where your connection reads through the API.',
    )
    expect(preview.textContent).toContain('Your connection already reads every one of them.')
  })

  it('asks which of them the connection’s own query matches when the query changes', async () => {
    const old = {
      formula: { v: 1, all: [{ any: [{ var: 'subject', op: 'contains', value: 'flight' }] }] },
      id: 'qry_flights',
      sentence: sentence('Flights'),
      model: false,
    }
    const wider = {
      formula: { v: 1, all: [] },
      id: 'qry_everything',
      sentence: sentence('Everything'),
      model: false,
    }
    server.on(
      `POST /api/v1/mailboxes/${personal.id}/matches`,
      matchesAs(
        ok({ messages: [received(1, '2026-11-10T09:00:00Z')], needs_model: [], unknown: [] }),
      ),
    )
    await consent(
      replacing(
        { query: old, mailboxes: [personal.id], since: { new: false } },
        {
          query: wider,
          mailboxes: [personal.id],
          draft: { mailboxes: [personal.id], since: { new: false } },
        },
      ),
      byMailbox({
        [personal.id]: [received(1, '2026-11-10T09:00:00Z'), received(2, '2026-11-09T09:00:00Z')],
      }),
    )
    await waitFor(() =>
      expect(rows()).toEqual([
        ['Subject 1', false],
        ['Subject 2', true],
      ]),
    )
    // The old query, with the received times of the page around it: the owner's own preview of a formula.
    const asked = server.asked(`POST /api/v1/mailboxes/${personal.id}/matches`)
    expect(asked).toHaveLength(1)
    const formula = (
      asked[0].body as { formula: { all: { any: { var: string; op: string }[] }[] } }
    ).formula
    expect(formula.all[0]).toEqual(old.formula.all[0])
    expect(formula.all.slice(1).map((g) => g.any.map((c) => `${c.var} ${c.op}`))).toEqual([
      ['received since'],
      ['received before'],
    ])
    // The formula as sent is the one web/tests/formula's Go test parses with internal/query: the two cannot drift.
    expect(formula).toEqual(reach)
  })

  it('marks every one new when it cannot tell what the connection’s query matches', async () => {
    const old = {
      formula: { v: 1, all: [{ any: [{ var: 'message', op: 'about', value: 'travel' }] }] },
      id: 'qry_travel',
      sentence: sentence('About travel'),
      model: true,
    }
    server.on(
      `POST /api/v1/mailboxes/${personal.id}/matches`,
      ok({ messages: [], needs_model: [summary(1).id], unknown: [] }),
    )
    await consent(
      replacing(
        { query: old, mailboxes: [personal.id], since: { new: false } },
        { draft: { mailboxes: [personal.id], since: { new: false } } },
      ),
      byMailbox({ [personal.id]: [received(1, '2026-11-10T09:00:00Z')] }),
    )
    await waitFor(() => expect(rows()).toEqual([['Subject 1', true]]))
    expect(screen.getByRole('region', { name: 'Preview' }).textContent).toContain(
      "1 of them your connection doesn't read, or may not: a model decides its query. They're marked new.",
    )
  })

  it('compares nothing when the query is the connection’s own, and marks nothing for a device added', async () => {
    const same = {
      formula: { v: 1, all: [] },
      id: 'qry_same',
      sentence: sentence('Same'),
      model: false,
    }
    await consent(
      replacing(
        { query: same, mailboxes: [personal.id], since: { new: false }, parts: ['sender'] },
        { query: same, draft: { mailboxes: [personal.id], since: { new: false } } },
      ),
      byMailbox({ [personal.id]: [received(1, '2026-11-10T09:00:00Z')] }),
    )
    await screen.findByText('Subject 1')
    expect(rows()).toEqual([['Subject 1', false]])
    expect(server.asked(`POST /api/v1/mailboxes/${personal.id}/matches`)).toHaveLength(0)

    await consent(
      requestOf({
        connection: connectionOf(),
        change: 'add_device',
        snapshot: snapshotOf({ change: 'add_device' }),
        draft: { mailboxes: [personal.id], since: { new: false } },
      }),
      ok(previewOf([received(1, '2026-10-01T09:00:00Z')], { change: 'add_device' })),
    )
    await screen.findByText('Subject 1')
    expect(rows()).toEqual([['Subject 1', false]])
    expect(screen.getByRole('region', { name: 'Preview' }).textContent).not.toContain('marked new')
  })

  /**
   * The request began naming connection A (MCP, both mailboxes); before its preview, another tab replaced A with B
   * (the API, the personal mailbox only). The preview names B, and Allow sends B's snapshot: the marks compare with B.
   */
  function replacedInAnotherTab() {
    return replacing(
      { id: 'con_original', mailboxes: [personal.id, work.id], since: { new: false } },
      {
        resource: 'api',
        draft: { mailboxes: [work.id], since: { new: false } },
        snapshot: snapshotOf({ change: 'replace', connection: 'con_original', version: 7 }),
      },
    )
  }
  const replacement = connectionOf({
    id: 'con_replacement',
    resource: 'api',
    mailboxes: [personal.id],
    since: { new: false },
    version: 1,
  })
  const replacementRoute = 'GET /api/v1/connections/con_replacement'

  it('compares with the connection the preview’s snapshot names, not the one the request began with', async () => {
    server
      .on(replacementRoute, ok(replacement))
      .on(decisionRoute, refused(409, 'too_many_connections'))
    const { user } = await consent(
      replacedInAnotherTab(),
      byMailbox(
        { [work.id]: [received(1, '2026-11-10T09:00:00Z')] },
        { connection: 'con_replacement', version: 1 },
      ),
    )
    await screen.findByText('Subject 1')
    await settle()
    // B cannot read the work mailbox: the message is new, and nothing is said of A's resource.
    expect(rows()).toEqual([['Subject 1', true]])
    const preview = screen.getByRole('region', { name: 'Preview' }).textContent
    expect(preview).toContain("1 of them your connection doesn't read. They're marked new.")
    expect(preview).not.toContain('already reads')
    expect(preview).not.toContain('reads through MCP')
    await user.click(allowButton())
    await waitFor(() => expect(server.asked(decisionRoute)).toHaveLength(1))
    expect(server.asked(decisionRoute)[0].body).toMatchObject({
      snapshot: { connection: 'con_replacement', version: 1 },
    })
  })

  it.each<[string, Reply]>([
    [
      'is not that version any more',
      ok({ ...replacement, mailboxes: [personal.id, work.id], version: 2 }),
    ],
    ['cannot be had', refused(404, 'not_found')],
  ])('marks every one new when the connection the snapshot names %s', async (_, reply) => {
    server.on(replacementRoute, reply)
    await consent(
      replacedInAnotherTab(),
      byMailbox(
        { [work.id]: [received(1, '2026-11-10T09:00:00Z')] },
        { connection: 'con_replacement', version: 1 },
      ),
    )
    await screen.findByText('Subject 1')
    await settle()
    expect(rows()).toEqual([['Subject 1', true]])
    const preview = screen.getByRole('region', { name: 'Preview' }).textContent
    expect(preview).toContain(
      'Your connection changed while this screen was open, so every one is marked new.',
    )
    expect(preview).not.toContain('reads through')
  })
})
