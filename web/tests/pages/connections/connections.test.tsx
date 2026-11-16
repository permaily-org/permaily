import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { MODEL_NOTICE_VERSION, modelNotice } from '../../../src/lib/model-notice.ts'
import { named, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import {
  connectionOf,
  device2,
  ended,
  signedInWithMailboxes,
  tripApp,
} from '../../helpers/connections.ts'
import { personal, work } from '../../helpers/mailboxes.ts'
import {
  ana,
  confirmationNeeded,
  meOf,
  network,
  ok,
  once,
  refused,
  server,
} from '../../helpers/server.ts'

type User = Awaited<ReturnType<typeof open>>['user']

const claude = connectionOf()
const list = 'GET /api/v1/connections'
const one = (id: string) => `GET /api/v1/connections/${id}`

function connections(items: unknown[] = [claude, tripApp, ended]) {
  return signedInWithMailboxes().on(list, ok({ connections: items }))
}

async function openConnection(c: Record<string, unknown> = claude, items?: unknown[]) {
  connections(items ?? [c, tripApp, ended]).on(one(c.id as string), ok(c))
  const opened = await open(`/connections/${(c.vendor as { domain: string }).domain}/${c.id}`)
  await screen.findByRole('heading', { level: 1, name: /reads your mail\.$/ })
  return opened
}

/** Confirms with the code of the message, in the dialog that asks for it. */
async function confirm(user: User) {
  const dialog = within(await screen.findByRole('dialog', { name: "Confirm it's you" }))
  await user.click(await dialog.findByRole('button', { name: 'Email me a code' }))
  await user.type(await dialog.findByLabelText('Code from the message'), '123456')
  await user.click(dialog.getByRole('button', { name: 'Confirm' }))
}

describe('V55: the connections list', () => {
  it('lists connections by vendor, the active and the ended ones', async () => {
    connections()
    await open('/connections')
    await screen.findByRole('heading', { level: 1, name: '2 connections.' })
    const claudeSection = within(screen.getByRole('region', { name: 'claude.ai' }))
    expect(claudeSection.getByText('Verified:', { exact: false })).toBeTruthy()
    expect(claudeSection.getAllByRole('listitem')).toHaveLength(2)
    expect(
      claudeSection.getAllByRole('link', { name: 'Claude Code' })[0].getAttribute('href'),
    ).toBe('/connections/claude.ai/con_claudecode')
    expect(claudeSection.getByText('A new connection replaced it')).toBeTruthy()
    const trips = within(screen.getByRole('region', { name: 'trips.example' }))
    expect(trips.getByText('The API, for apps')).toBeTruthy()
    expect(trips.queryByText(/Verified/)).toBeNull()
    await expectAccessible()
  })

  it('says when there is none', async () => {
    connections([])
    await open('/connections')
    await screen.findByRole('heading', { level: 1, name: 'No connections.' })
  })

  it('says when the list could not load, and tries again', async () => {
    signedInWithMailboxes().on(list, once(network, ok({ connections: [claude] })))
    const { user } = await open('/connections')
    await screen.findByText(/Your connections couldn't load\./)
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    await screen.findByRole('heading', { level: 1, name: '1 connection.' })
  })

  it('opens a vendor on its overview, with a tab for each connection', async () => {
    connections()
    await open('/connections')
    await screen.findByRole('heading', { level: 1, name: '2 connections.' })
    // Its address does not end in the vendor's domain, which the app's server would take for a file (internal/webui).
    const vendor = within(screen.getByRole('region', { name: 'claude.ai' })).getByRole('link', {
      name: 'claude.ai',
    })
    expect(vendor.getAttribute('href')).toBe('/connections/claude.ai/overview')
    await open('/connections/claude.ai/overview')
    await screen.findByRole('heading', { level: 1, name: '2 connections.' })
    const tabs = within(screen.getByRole('navigation', { name: 'Connections of claude.ai' }))
    expect(tabs.getAllByRole('link').map((l) => l.textContent)).toEqual([
      'Overview',
      'Claude Code',
      'Claude Code, ended',
    ])
    expect(tabs.getByRole('link', { name: 'Overview' }).getAttribute('href')).toBe(
      '/connections/claude.ai/overview',
    )
    await expectAccessible()
  })

  it('cannot find a vendor or a connection it does not have', async () => {
    connections()
    await open('/connections/nobody.example/overview')
    await screen.findByRole('heading', { name: "We can't find that connection." })
    await open('/connections/claude.ai/con_nobody')
    await screen.findByRole('heading', { name: "We can't find that connection." })
  })
})

describe('V55: one connection', () => {
  it('shows its sentence, parts, mailboxes, mail since, end and devices', async () => {
    await openConnection(tripApp)
    const main = screen.getByRole('main')
    expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('Trip App reads your mail.')
    expect(main.textContent).toContain('allowed 1 Nov 2026, ends 30 Jan 2027')
    expect(screen.getByText('Flight and hotel bookings')).toBeTruthy()
    expect(main.textContent).toContain('Who sent it and when and the subject')
    await screen.findByText('Personal')
    expect(main.textContent).toContain('Mail since 1 Oct 2026')
    expect(main.textContent).toContain('The API, for apps')
    expect(main.textContent).toContain('Not verified by Permaily')
    const devices = within(screen.getByRole('region', { name: 'Devices' }))
    expect(devices.getAllByRole('listitem')).toHaveLength(1)
    await expectAccessible()
  })

  it('a connection without a query reads all mail of its mailboxes, and says which devices read less', async () => {
    await openConnection()
    expect(screen.getByText('All mail')).toBeTruthy()
    await screen.findByText('Work')
    expect(screen.getByRole('main').textContent).toContain('New mail only, from 1 Nov 2026')
    const devices = within(screen.getByRole('region', { name: 'Devices' }))
    expect(devices.getAllByRole('listitem')).toHaveLength(2)
    expect(devices.getByText(/Reads less than the connection/)).toBeTruthy()
  })

  it('says plainly when it gets no mail because the model cannot be asked', async () => {
    await openConnection(connectionOf({ delivery: 'model_unavailable' }))
    expect(screen.getByText('It gets no mail now.')).toBeTruthy()
    expect(
      screen.getByText(
        "Permaily can't ask its model (no key, or a spend limit reached), so nothing leaves until it can.",
      ),
    ).toBeTruthy()
    await expectAccessible()
  })

  it('says plainly when the model’s notice changed, and renews it under the one in force after a fresh proof', async () => {
    const renewed = connectionOf({
      delivery: 'delivering',
      ends_at: '2027-02-14T10:00:00Z',
      model_notice: MODEL_NOTICE_VERSION,
      version: 8,
    })
    server
      .on('POST /api/v1/connections/con_claudecode/renew', once(confirmationNeeded, ok(renewed)))
      .on('GET /api/v1/auth/confirm', ok({ ways: ['email'] }))
      .on('POST /api/v1/auth/confirm/email/start', ok({ attempt: 'att_c', expires_in: 600 }))
      .on(
        'POST /api/v1/auth/confirm/email/finish',
        ok(meOf(ana, { confirmed_until: '2026-11-12T10:10:00Z' })),
      )
    const { user } = await openConnection(
      connectionOf({ delivery: 'model_notice_changed', model_notice: '2026-01-01' }),
    )
    expect(screen.getByText('It gets no mail until you renew it.')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: named('Renew Claude Code') }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Renew Claude Code?' }))
    // The notice in force is shown before renewing under it.
    for (const paragraph of modelNotice) expect(dialog.getByText(paragraph)).toBeTruthy()
    await user.click(dialog.getByRole('button', { name: 'Renew' }))
    await confirm(user)
    await screen.findByText('Renewed. It ends 14 Feb 2027.')
    const asked = server.asked('POST /api/v1/connections/con_claudecode/renew')
    expect(asked).toHaveLength(2)
    expect(asked[1].body).toEqual({ model_notice: MODEL_NOTICE_VERSION, version: 7 })
    expect(screen.queryByText('It gets no mail until you renew it.')).toBeNull()
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('heading', { level: 1 })),
    )
    await expectAccessible()
  })

  it('shows the connection again when it changed since the page showed it', async () => {
    server.on('POST /api/v1/connections/con_claudecode/renew', refused(409, 'consent_changed'))
    const { user } = await openConnection()
    await user.click(screen.getByRole('button', { name: named('Renew Claude Code') }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Renew Claude Code?' }))
    await user.click(dialog.getByRole('button', { name: 'Renew' }))
    await screen.findByText(
      'What allowing does changed since this page opened. Read it again, then decide.',
    )
    await waitFor(() => expect(server.asked(one('con_claudecode'))).toHaveLength(2))
  })

  it('revokes it, after asking, with no fresh proof, and says why it ended', async () => {
    const revoked = connectionOf({
      state: 'ended',
      ended_at: '2026-11-12T10:00:00Z',
      end_reason: 'person',
      devices: claude.devices.map((d) => ({ ...d, state: 'ended', end_reason: 'person' })),
    })
    server.on('POST /api/v1/connections/con_claudecode/revoke', ok(revoked))
    const { user } = await openConnection()
    await user.click(screen.getByRole('button', { name: named('Revoke Claude Code') }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Revoke Claude Code?' }))
    await expectAccessible()
    await user.click(dialog.getByRole('button', { name: 'Revoke' }))
    await screen.findByRole('heading', { level: 1, name: 'Claude Code no longer reads your mail.' })
    expect(screen.getByText('You revoked it.')).toBeTruthy()
    expect(screen.queryByRole('button', { name: named('Renew Claude Code') })).toBeNull()
    expect(screen.queryByRole('button', { name: /^Revoke/ })).toBeNull()
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('heading', { level: 1 })),
    )
    // The tab follows.
    expect(
      within(screen.getByRole('navigation', { name: 'Connections of claude.ai' })).getAllByText(
        ', ended',
      ),
    ).toHaveLength(2)
  })

  it('cancelling the question revokes nothing', async () => {
    const { user } = await openConnection()
    await user.click(screen.getByRole('button', { name: named('Revoke Claude Code') }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Revoke Claude Code?' }))
    await user.click(dialog.getByRole('button', { name: 'Cancel' }))
    await settle()
    expect(server.asked('POST /api/v1/connections/con_claudecode/revoke')).toHaveLength(0)
  })

  it('revokes one device, and the others keep working', async () => {
    const after = connectionOf({
      devices: [
        claude.devices[0],
        { ...device2, state: 'ended', ended_at: '2026-11-12T10:00:00Z', end_reason: 'person' },
      ],
    })
    server.on('POST /api/v1/connections/con_claudecode/devices/dev_two/revoke', ok(after))
    const { user } = await openConnection()
    await user.click(screen.getByRole('button', { name: named('Revoke device 2') }))
    await screen.findByText('Device 2 revoked. The others keep working.')
    expect(screen.queryByRole('button', { name: named('Revoke device 2') })).toBeNull()
    expect(screen.getByRole('button', { name: named('Revoke device 1') })).toBeTruthy()
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('heading', { name: 'Devices' })),
    )
    await expectAccessible()
  })

  it('says why a device could not be revoked', async () => {
    server.on(
      'POST /api/v1/connections/con_claudecode/devices/dev_two/revoke',
      refused(409, 'connection_ended'),
    )
    const { user } = await openConnection()
    await user.click(screen.getByRole('button', { name: named('Revoke device 2') }))
    await screen.findByText('That connection ended.')
  })

  it('an ended one says why, and leads to the one that replaced it', async () => {
    connections().on(one('con_old'), ok(ended))
    await open('/connections/claude.ai/con_old')
    await screen.findByRole('heading', { level: 1, name: 'Claude Code no longer reads your mail.' })
    expect(screen.getByText('A new connection replaced it.')).toBeTruthy()
    expect(
      screen.getByRole('link', { name: 'See the one that replaced it' }).getAttribute('href'),
    ).toBe('/connections/claude.ai/con_claudecode')
    expect(screen.queryByRole('button', { name: /^(Renew|Revoke)/ })).toBeNull()
    await expectAccessible()
  })

  it('names a mailbox it no longer knows by its identifier', async () => {
    connections().on(one('con_claudecode'), ok(connectionOf({ mailboxes: ['mbx_gone'] })))
    server.on('GET /api/v1/mailboxes', ok({ mailboxes: [personal, work], limit: 5 }))
    await open('/connections/claude.ai/con_claudecode')
    await screen.findByText('mbx_gone')
  })

  it('says when one connection could not load', async () => {
    connections().on(one('con_claudecode'), once(network, ok(claude)))
    const { user } = await open('/connections/claude.ai/con_claudecode')
    await screen.findByText(/This connection couldn't load\./)
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    await screen.findByRole('heading', { level: 1, name: 'Claude Code reads your mail.' })
  })
})
