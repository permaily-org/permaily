import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { current, named, open } from '../helpers/app.tsx'
import { expectAccessible } from '../helpers/axe.ts'
import { messageOf, personal, personalAddress, summaryOf } from '../helpers/mailboxes.ts'
import { type Account, ana, meOf, network, ok, server } from '../helpers/server.ts'

const closing: Account = {
  ...ana,
  state: 'closing',
  closes_at: '2026-11-18T14:32:00Z',
  closing_by: 'person',
}
const list = 'GET /api/v1/mailboxes'
const first = 'GET /api/v1/mailboxes/mbx_personal/messages?limit=50'
const one = messageOf()

function asClosing() {
  vi.useFakeTimers({ toFake: ['Date'], now: Date.parse('2026-11-12T10:00:00Z') })
  return server.on('GET /api/v1/me', ok(meOf(closing)))
}

/** Signed in to an account that agreed to no terms, while some are in force. */
function asNotAgreed(account: Account = ana) {
  return server.on('GET /api/v1/me', ok(meOf(account, {}, '2027-01-01')))
}

function mailbox() {
  return server
    .on(list, ok({ mailboxes: [personal], limit: 5 }))
    .on(first, ok({ messages: [summaryOf(1)] }))
    .on('GET /api/v1/mailboxes/mbx_personal', ok(personal))
    .on(`GET /api/v1/mailboxes/mbx_personal/messages/${one.id}`, ok(one))
}

describe('W5: a closing account reaches its mailboxes, and only what it may do there', () => {
  it('lists its mailboxes under the banner, with disconnecting and nothing that changes them', async () => {
    asClosing()
    mailbox()
    await open('/accounts')
    await screen.findByText(personalAddress)
    const banner = screen.getByRole('region', { name: 'Your account is being deleted' })
    expect(within(banner).getByText(/^Your account is being deleted/).textContent).toBe(
      'Your account is being deleted on Wednesday, 18 November 2026. Until then you can read and download your mail and disconnect mailboxes.',
    )
    expect(within(banner).getByRole('button', { name: 'Cancel deletion' })).toBeTruthy()
    // No connecting, renaming or retention: the server refuses them.
    expect(screen.queryByRole('link', { name: 'Connect a mailbox' })).toBeNull()
    expect(screen.queryByRole('button', { name: /^Rename/ })).toBeNull()
    expect(screen.queryByLabelText('Keep mail for')).toBeNull()
    expect(screen.getByText('90 days')).toBeTruthy()
    expect(screen.getByRole('button', { name: named('Disconnect Personal') })).toBeTruthy()
    // The sidebar of the app is not there: only the mailboxes.
    expect(screen.queryByRole('navigation', { name: 'Main' })).toBeNull()
    const nav = within(screen.getByRole('navigation', { name: 'Mailboxes' }))
    expect(nav.getByRole('link', { name: 'Inbox' }).getAttribute('href')).toBe('/inbox')
    await expectAccessible()
  })

  it('reads and searches its mail, and deletes none', async () => {
    asClosing()
    mailbox().on(`${first}&q=x`, ok({ messages: [] }))
    const { user } = await open('/inbox/mbx_personal')
    await screen.findByText('Sender 1')
    expect(screen.getByRole('searchbox', { name: 'Search this mailbox' })).toBeTruthy()
    expect(screen.queryByRole('checkbox')).toBeNull()
    expect(screen.queryByRole('button', { name: named('Delete selected') })).toBeNull()
    await user.click(screen.getByRole('link', { name: /Sender 1/ }))
    await screen.findByRole('heading', { level: 1, name: 'Dinner on Friday' })
    expect(screen.getByRole('link', { name: 'Download original' })).toBeTruthy()
    expect(screen.queryByRole('button', { name: 'Delete' })).toBeNull()
    await expectAccessible()
  })

  it('cancels the deletion from the banner, and the app is back', async () => {
    asClosing()
    mailbox().on('POST /api/v1/account/delete/cancel', network)
    const { user } = await open('/accounts')
    await screen.findByText(personalAddress)
    await user.click(screen.getByRole('button', { name: 'Cancel deletion' }))
    await screen.findByText(
      "The deletion wasn't cancelled. Permaily can't be reached. Check your connection, then try again.",
    )
    server.on('POST /api/v1/account/delete/cancel', ok(ana))
    await user.click(screen.getByRole('button', { name: 'Cancel deletion' }))
    await screen.findByRole('navigation', { name: 'Main' })
    expect(screen.queryByRole('region', { name: 'Your account is being deleted' })).toBeNull()
    await screen.findByText('Deletion cancelled. Your account works again.')
    expect(screen.getByRole('link', { name: 'Connect a mailbox' })).toBeTruthy()
    expect(current()).toBe('/accounts')
  })

  it('still shows the closing screen everywhere else, which leads to the mailboxes', async () => {
    asClosing()
    mailbox()
    const { user } = await open('/settings')
    await screen.findByRole('heading', { name: 'Your account is being deleted' })
    await user.click(screen.getByRole('link', { name: 'Your mailboxes' }))
    await screen.findByText(personalAddress)
    expect(current()).toBe('/accounts')
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('heading', { level: 1 })),
    )
    await open('/accounts/new')
    await screen.findByRole('heading', { name: 'Your account is being deleted' })
    await open('/rules')
    await screen.findByRole('heading', { name: 'Your account is being deleted' })
  })
})

describe('W5: an account that has not agreed reaches the list, downloads and disconnecting, nothing else', () => {
  it('lists its mailboxes under the banner, which leads to the terms', async () => {
    asNotAgreed()
    mailbox()
    const { user } = await open('/accounts')
    await screen.findByText(personalAddress)
    const banner = screen.getByRole('region', { name: 'The terms changed' })
    expect(banner.textContent).toContain(
      'Agree to the new terms to use Permaily. Until then you can download your mail and disconnect your mailboxes.',
    )
    expect(screen.queryByRole('link', { name: 'Connect a mailbox' })).toBeNull()
    expect(screen.queryByRole('button', { name: /^Rename/ })).toBeNull()
    expect(screen.getByRole('button', { name: named('Disconnect Personal') })).toBeTruthy()
    await expectAccessible()
    await user.click(within(banner).getByRole('link', { name: 'Read and agree' }))
    await screen.findByRole('heading', { name: 'The terms changed' })
    await user.click(screen.getByRole('link', { name: 'Your mailboxes' }))
    await screen.findByText(personalAddress)
  })

  it('lists its messages with Download for each, and offers no search, no opening and no deleting', async () => {
    asNotAgreed()
    mailbox()
    await open('/inbox/mbx_personal')
    await screen.findByText('Sender 1')
    expect(screen.queryByRole('searchbox')).toBeNull()
    expect(screen.queryByRole('checkbox')).toBeNull()
    expect(screen.queryByRole('link', { name: /Sender 1/ })).toBeNull()
    const download = screen.getByRole('link', { name: named('Download Subject 1') })
    expect(download.getAttribute('href')).toBe(
      `/api/v1/mailboxes/mbx_personal/messages/${summaryOf(1).id}/raw`,
    )
    expect(download.hasAttribute('download')).toBe(true)
    await expectAccessible()
  })

  it('does not open a message, and offers its download', async () => {
    asNotAgreed()
    mailbox()
    await open(`/inbox/mbx_personal/${one.id}`)
    await screen.findByText(
      'Agree to the new terms to open messages. Until then you can download it.',
    )
    expect(screen.getByRole('link', { name: 'Download original' })).toBeTruthy()
    expect(server.asked(`GET /api/v1/mailboxes/mbx_personal/messages/${one.id}`)).toHaveLength(0)
    await expectAccessible()
  })

  it('still shows the terms screen everywhere else', async () => {
    asNotAgreed()
    await open('/accounts/new')
    await screen.findByRole('heading', { name: 'The terms changed' })
    await open('/connections')
    await screen.findByRole('heading', { name: 'The terms changed' })
  })

  it('shows both banners to an account that is closing and has not agreed', async () => {
    vi.useFakeTimers({ toFake: ['Date'], now: Date.parse('2026-11-12T10:00:00Z') })
    asNotAgreed(closing)
    mailbox()
    await open('/inbox/mbx_personal')
    await screen.findByText('Sender 1')
    expect(screen.getByRole('region', { name: 'Your account is being deleted' })).toBeTruthy()
    expect(screen.getByRole('region', { name: 'The terms changed' })).toBeTruthy()
    // What both allow: listing and downloading.
    expect(screen.queryByRole('searchbox')).toBeNull()
    expect(screen.getByRole('link', { name: named('Download Subject 1') })).toBeTruthy()
  })
})

describe('W5: every other route', () => {
  it('shows the sidebar with Inbox first to an account that is open and agreed', async () => {
    server.signedIn().on(list, ok({ mailboxes: [], limit: 5 }))
    await open('/inbox')
    const nav = within(await screen.findByRole('navigation', { name: 'Main' }))
    // A section's name, without what waits there.
    expect(
      nav.getAllByRole('link').map((link) => link.textContent?.replace(/\d+ waiting for you$/, '')),
    ).toEqual(['Inbox', 'Connections', 'Rules', 'Activity', 'Accounts', 'Settings'])
    expect(screen.queryByRole('region', { name: /deleted|terms/ })).toBeNull()
  })
})
