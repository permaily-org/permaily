import { screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { expectNotKept, named, open, settle, watchLeaving } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import {
  allowedRedirect,
  decisionRoute,
  previewOf,
  previewRoute,
  requestOf,
  signedInWithMailboxes,
  startRoute,
  vendorQuery,
} from '../../helpers/connections.ts'
import { ok, refused, server } from '../../helpers/server.ts'

// K54 (ADR 0039, pull request 5): the consent screen shows the webhook a vendor asked for, its host, when it is told,
// that it is never told the mail, and for a schedule the person's time zone, preselected from the browser with a
// picker of the zones the browser knows, sent with the approval. Against the contract and a played server.

const weekly = { every: 'week', on: 'monday', at: '08:00' }

/** Trip App's request through the API, with a webhook that tells it of new mail and every Monday at 08:00. */
function tripRequest(
  webhook: Record<string, unknown> | null = {
    host: 'trip-app.com',
    new_mail: true,
    schedule: weekly,
  },
) {
  return requestOf({
    vendor: { domain: 'trip-app.com' },
    client_name: 'Trip App',
    redirect_host: 'trip-app.com',
    loopback: false,
    resource: 'api',
    ...(webhook ? { webhook } : {}),
  })
}

/** The browser's own time zone, as `Intl` says it. */
function browserZone(zone: string) {
  const real = Intl.DateTimeFormat.prototype.resolvedOptions
  vi.spyOn(Intl.DateTimeFormat.prototype, 'resolvedOptions').mockImplementation(function (
    this: Intl.DateTimeFormat,
  ) {
    return { ...real.call(this), timeZone: zone }
  })
}

afterEach(() => {
  vi.restoreAllMocks()
})

async function consent(request: Record<string, unknown>) {
  signedInWithMailboxes().on(startRoute, ok({ request })).on(previewRoute, ok(previewOf()))
  const opened = await open(`/oauth/authorize?${vendorQuery}`)
  await screen.findByRole('heading', { name: /asks to read your mail\.$/ })
  return opened
}

function allowButton() {
  return screen.getByRole('button', { name: named('Allow Trip App') })
}

/** The decision the screen sent, once the browser left. */
async function allowed(user: Awaited<ReturnType<typeof open>>['user']) {
  const leave = watchLeaving()
  server.on(decisionRoute, ok({ redirect: allowedRedirect }))
  await waitFor(() => expect(allowButton().getAttribute('aria-disabled')).toBe('false'))
  await user.click(allowButton())
  await waitFor(() => expect(leave).toHaveBeenCalledWith(allowedRedirect))
  return server.asked(decisionRoute)[0].body as Record<string, unknown>
}

describe('K54: the webhook on the consent screen', () => {
  it('says whom it tells and when, in the browser’s zone, that it is never told the mail, and sends the zone', async () => {
    browserZone('Europe/Madrid')
    const { user } = await consent(tripRequest())
    const term = screen.getByText('Tells').parentElement as HTMLElement
    expect(term.textContent).toContain('Tells trip-app.com when new mail it may read arrives')
    expect(term.textContent).toContain(
      'Tells trip-app.com every Monday at 08:00, your time (Europe/Madrid)',
    )
    expect(term.textContent).toContain(
      'trip-app.com is told only that there is mail to fetch, never the mail. It fetches with this connection, and every message it gets is in your activity.',
    )
    // The zone is preselected from the browser, among the zones it knows.
    const picker = screen.getByRole('combobox', { name: 'Your time zone' }) as HTMLSelectElement
    expect(picker.value).toBe('Europe/Madrid')
    expect(within(picker).getByRole('option', { name: 'America/New_York' })).toBeTruthy()
    expect(picker.getAttribute('aria-describedby')).toBeTruthy()
    expect(
      screen.getByText(/The schedule follows this time zone\. The app isn't told it\./),
    ).toBeTruthy()
    // Nothing on the screen removes the webhook from the request: it is accepted or refused as a whole.
    expect(screen.queryByRole('checkbox', { name: /tell|webhook/i })).toBeNull()
    await expectAccessible()

    // Another zone: the sentence follows, and the approval sends it.
    await user.selectOptions(picker, 'America/New_York')
    expect(term.textContent).toContain('every Monday at 08:00, your time (America/New_York)')
    const decision = await allowed(user)
    expect(decision.allow).toBe(true)
    expect(decision.time_zone).toBe('America/New_York')
    // The zone is the person's: kept nowhere in the browser.
    expectNotKept('America/New_York')
  })

  it('says a schedule every day, and sends the browser’s zone when the person keeps it', async () => {
    browserZone('Australia/Lord_Howe')
    const { user } = await consent(
      tripRequest({ host: 'trip-app.com', schedule: { every: 'day', at: '17:45' } }),
    )
    const term = screen.getByText('Tells').parentElement as HTMLElement
    expect(term.textContent).toContain(
      'Tells trip-app.com every day at 17:45, your time (Australia/Lord_Howe)',
    )
    expect(term.textContent).not.toContain('new mail')
    expect((await allowed(user)).time_zone).toBe('Australia/Lord_Howe')
  })

  it('asks no zone of a webhook told only of new mail, and sends none', async () => {
    browserZone('Europe/Madrid')
    const { user } = await consent(tripRequest({ host: 'trip-app.com', new_mail: true }))
    expect(screen.getByText('Tells').parentElement?.textContent).toContain(
      'Tells trip-app.com when new mail it may read arrives',
    )
    expect(screen.queryByRole('combobox', { name: 'Your time zone' })).toBeNull()
    expect(screen.getByRole('main').textContent).not.toContain('your time')
    expect((await allowed(user)).time_zone).toBeUndefined()
  })

  it('shows nothing new for a request without a webhook, and sends no zone', async () => {
    const { user } = await consent(tripRequest(null))
    expect(screen.queryByText('Tells')).toBeNull()
    expect(screen.queryByRole('combobox', { name: 'Your time zone' })).toBeNull()
    expect((await allowed(user)).time_zone).toBeUndefined()
  })

  it('offers the browser’s zone even when its list leaves it out, and asks for one when the browser says none', async () => {
    browserZone('Etc/Unknown_Zone')
    vi.spyOn(Intl, 'supportedValuesOf').mockReturnValue(['Europe/Madrid'])
    await consent(tripRequest())
    const picker = screen.getByRole('combobox', { name: 'Your time zone' }) as HTMLSelectElement
    expect(picker.value).toBe('Etc/Unknown_Zone')
    expect([...picker.options].map((o) => o.value)).toEqual(['Etc/Unknown_Zone', 'Europe/Madrid'])
  })

  it('does not allow a schedule until a zone is chosen when the browser gives none', async () => {
    browserZone('')
    vi.spyOn(Intl, 'supportedValuesOf').mockImplementation(() => {
      throw new RangeError('not supported')
    })
    const { user } = await consent(tripRequest())
    const picker = screen.getByRole('combobox', { name: 'Your time zone' }) as HTMLSelectElement
    expect(picker.value).toBe('')
    expect(screen.getByText('Choose your time zone.')).toBeTruthy()
    expect(screen.getByText('Tells').parentElement?.textContent).toContain(
      'every Monday at 08:00, your time',
    )
    await waitFor(() => expect(allowButton().getAttribute('aria-disabled')).toBe('true'))
    await user.click(allowButton())
    await settle()
    expect(server.asked(decisionRoute)).toHaveLength(0)
    // Review of PR #185, finding 1: the picker still offers zones to choose, and choosing one allows.
    await user.selectOptions(picker, 'Europe/Lisbon')
    expect((await allowed(user)).time_zone).toBe('Europe/Lisbon')
  })

  it('offers zones to choose from when the browser cannot list them (review of PR #185, finding 1)', async () => {
    browserZone('Europe/Madrid')
    vi.spyOn(Intl, 'supportedValuesOf').mockImplementation(() => {
      throw new RangeError('not supported')
    })
    const { user } = await consent(tripRequest())
    const picker = screen.getByRole('combobox', { name: 'Your time zone' }) as HTMLSelectElement
    expect(picker.value).toBe('Europe/Madrid')
    const zones = [...picker.options].map((o) => o.value)
    expect(zones.length).toBeGreaterThan(50)
    expect(zones).toEqual([...zones].sort())
    expect(new Set(zones).size).toBe(zones.length)
    for (const zone of ['UTC', 'America/New_York', 'Asia/Tokyo', 'Australia/Sydney'])
      expect(zones).toContain(zone)
    await user.selectOptions(picker, 'America/New_York')
    expect((await allowed(user)).time_zone).toBe('America/New_York')
  })

  it('says in words when the server does not know the zone, and lets the person choose another', async () => {
    browserZone('Europe/Madrid')
    const { user } = await consent(tripRequest())
    server.on(decisionRoute, refused(400, 'time_zone_invalid'))
    await waitFor(() => expect(allowButton().getAttribute('aria-disabled')).toBe('false'))
    await user.click(allowButton())
    const alert = await screen.findByText(
      "That time zone isn't one Permaily knows. Choose another, then allow again.",
    )
    expect(alert.closest('[role="alert"]')).toBeTruthy()
    // The screen stays, the picker with it; another zone is sent the next time.
    const picker = screen.getByRole('combobox', { name: 'Your time zone' })
    await user.selectOptions(picker, 'Europe/Lisbon')
    const leave = watchLeaving()
    server.on(decisionRoute, ok({ redirect: allowedRedirect }))
    await user.click(allowButton())
    await waitFor(() => expect(leave).toHaveBeenCalledWith(allowedRedirect))
    const asked = server.asked(decisionRoute)
    expect(asked.map((a) => (a.body as { time_zone?: string }).time_zone)).toEqual([
      'Europe/Madrid',
      'Europe/Lisbon',
    ])
  })

  it('puts the vendor’s host in the page as text, isolated', async () => {
    browserZone('Europe/Madrid')
    await consent(tripRequest({ host: '<b>trip-app.com</b>', new_mail: true }))
    const term = screen.getByText('Tells').parentElement as HTMLElement
    expect(term.querySelector('b')).toBeNull()
    expect(term.textContent).toContain(
      'Tells <b>trip-app.com</b> when new mail it may read arrives',
    )
    expect(term.querySelectorAll('bdi').length).toBeGreaterThan(0)
  })
})
