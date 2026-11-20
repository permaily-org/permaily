import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { named, open, settle } from '../helpers/app.tsx'
import { expectAccessible } from '../helpers/axe.ts'
import {
  connectionOf,
  device2,
  noRules,
  startRoute,
  tripApp,
  vendorQuery,
} from '../helpers/connections.ts'
import { personal, work } from '../helpers/mailboxes.ts'
import { type Account, ana, meOf, ok, server } from '../helpers/server.ts'

/**
 * V62 (ADR 0030, finding 17): an account that has not agreed to the terms in force keeps its connections reading, so
 * it can always see them and revoke one or a device of one; it can neither approve a new one nor renew one.
 */

const claude = connectionOf()

function asNotAgreed(account: Account = ana) {
  return server
    .on('GET /api/v1/me', ok(meOf(account, {}, '2027-01-01')))
    .on('GET /api/v1/mailboxes', ok({ mailboxes: [personal, work], limit: 5 }))
    .on('GET /api/v1/rules', ok(noRules))
    .on('GET /api/v1/connections', ok({ connections: [claude, tripApp] }))
    .on('GET /api/v1/connections/con_claudecode', ok(claude))
}

describe('V62: the connections under unagreed terms', () => {
  it('lists them in the limited form, with the way to the agreement', async () => {
    asNotAgreed()
    await open('/connections')
    await screen.findByRole('heading', { level: 1, name: '2 connections.' })
    const banner = screen.getByRole('region', { name: 'The terms changed' })
    expect(within(banner).getByRole('link', { name: 'Read and agree' }).getAttribute('href')).toBe(
      '/',
    )
    // Not the app's sidebar: the limited shell, with what the account may still do.
    expect(screen.queryByRole('navigation', { name: 'Main' })).toBeNull()
    const nav = within(screen.getByRole('navigation', { name: 'What you can still do' }))
    expect(nav.getAllByRole('link').map((l) => l.textContent)).toEqual([
      'Connections',
      'Inbox',
      'Accounts',
    ])
    await expectAccessible()
  })

  it('reaches them from the screen that asks for the agreement', async () => {
    asNotAgreed()
    const { user } = await open('/settings')
    await screen.findByRole('heading', { name: 'The terms changed' })
    await user.click(screen.getByRole('link', { name: 'Your connections' }))
    await screen.findByRole('heading', { level: 1, name: '2 connections.' })
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('heading', { level: 1 })),
    )
  })

  it('shows one, revokes a device and the whole connection, and never offers renewing', async () => {
    const withoutTwo = connectionOf({
      devices: [claude.devices[0], { ...device2, state: 'ended', end_reason: 'person' }],
    })
    const revoked = connectionOf({ ...withoutTwo, state: 'ended', end_reason: 'person' })
    asNotAgreed()
      .on('POST /api/v1/connections/con_claudecode/devices/dev_two/revoke', ok(withoutTwo))
      .on('POST /api/v1/connections/con_claudecode/revoke', ok(revoked))
    const { user } = await open('/connections/claude.ai/con_claudecode')
    await screen.findByRole('heading', { level: 1, name: 'Claude Code reads your mail.' })
    expect(screen.queryByRole('button', { name: /^Renew/ })).toBeNull()
    await expectAccessible()

    await user.click(screen.getByRole('button', { name: named('Revoke device 2') }))
    await screen.findByText('Device 2 revoked. The others keep working.')
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('heading', { name: 'Devices' })),
    )

    await user.click(screen.getByRole('button', { name: named('Revoke Claude Code') }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Revoke Claude Code?' }))
    await user.click(dialog.getByRole('button', { name: 'Revoke' }))
    await screen.findByRole('heading', { level: 1, name: 'Claude Code no longer reads your mail.' })
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('heading', { level: 1 })),
    )
    await expectAccessible()
  })

  it('says a connection waiting for a renewal needs the agreement first, and offers no renewal', async () => {
    asNotAgreed().on(
      'GET /api/v1/connections/con_claudecode',
      ok(connectionOf({ delivery: 'model_notice_changed' })),
    )
    await open('/connections/claude.ai/con_claudecode')
    await screen.findByText('It gets no mail until you renew it.')
    expect(screen.getByRole('main').textContent).toContain(
      'Agree to the new terms first, then renew it.',
    )
    expect(screen.queryByRole('button', { name: /^Renew/ })).toBeNull()
  })

  it('does not reach the consent screen: approving waits for the agreement', async () => {
    asNotAgreed()
    await open(`/oauth/authorize?${vendorQuery}`)
    await screen.findByRole('heading', { name: 'The terms changed' })
    await settle()
    expect(server.asked(startRoute)).toHaveLength(0)
  })

  // Every spelling the router reads as the sample flow's route: the gate decides with the router's own matching.
  for (const path of [
    '/connections/new',
    '/connections/NEW',
    '/connections/new/',
    '/connections/%6eew',
  ]) {
    it(`does not reach the sample flow of connecting an agent at ${path}`, async () => {
      asNotAgreed()
      await open(path)
      await settle()
      expect(screen.getByRole('heading', { level: 1 }).textContent).toBe('The terms changed')
      expect(screen.queryByText('Connect an agent.')).toBeNull()
    })
  }

  it('reaches a vendor’s overview and a connection in every spelling the router reads', async () => {
    asNotAgreed()
    await open('/connections/claude.ai/overview')
    await screen.findByRole('heading', { level: 1, name: '1 connection.' })
    expect(screen.getByRole('region', { name: 'The terms changed' })).toBeTruthy()
    await open('/Connections/claude.ai/con_claudecode/')
    await screen.findByRole('heading', { level: 1, name: 'Claude Code reads your mail.' })
    expect(screen.getByRole('region', { name: 'The terms changed' })).toBeTruthy()
  })

  it('an account that is closing sees only that, its connections included', async () => {
    asNotAgreed({
      ...ana,
      state: 'closing',
      closes_at: '2026-11-18T14:32:00Z',
      closing_by: 'person',
    })
    await open('/connections')
    await screen.findByRole('heading', { name: 'Your account is being deleted' })
  })

  it('an account that agreed sees them in the app’s shell, with renewing', async () => {
    server
      .on('GET /api/v1/me', ok(meOf()))
      .on('GET /api/v1/mailboxes', ok({ mailboxes: [personal, work], limit: 5 }))
      .on('GET /api/v1/rules', ok(noRules))
      .on('GET /api/v1/rules', ok(noRules))
      .on('GET /api/v1/connections', ok({ connections: [claude] }))
      .on('GET /api/v1/connections/con_claudecode', ok(claude))
    await open('/connections/claude.ai/con_claudecode')
    await screen.findByRole('heading', { level: 1, name: 'Claude Code reads your mail.' })
    expect(screen.getByRole('navigation', { name: 'Main' })).toBeTruthy()
    expect(screen.queryByRole('region', { name: 'The terms changed' })).toBeNull()
    expect(screen.getByRole('button', { name: named('Renew Claude Code') })).toBeTruthy()
  })
})
