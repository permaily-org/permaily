import { screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { asAdmin } from '../../helpers/admin.ts'
import { open } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { network, ok, refused, server } from '../../helpers/server.ts'

const first = 'GET /api/v1/admin/log?limit=50'
const by = { id: 'acc_boss', address: 'boss@example.org' }
const kinds = [
  'block',
  'unblock',
  'delete',
  'delete_cancel',
  'sessions_close',
  'invitations_made',
  'invitation_revoked',
  'provider_app_set',
  'provider_app_removed',
  'admin_made',
  'admin_removed',
  'something_new',
]

async function activity() {
  const opened = await open('/admin/activity')
  await screen.findByRole('heading', { level: 1, name: 'Admin activity' })
  return opened
}

function log() {
  return within(screen.getByRole('region', { name: 'What admins did' }))
}

describe("the admins' activity", () => {
  it('names the provider whose application was set or removed, and one it does not know as before', async () => {
    asAdmin().on(
      first,
      ok({
        entries: [
          { at: '2026-11-12T09:00:00Z', kind: 'provider_app_set', admin: by, provider: 'meta' },
          {
            at: '2026-11-12T08:00:00Z',
            kind: 'provider_app_removed',
            admin: by,
            provider: 'github',
          },
          { at: '2026-11-12T07:00:00Z', kind: 'provider_app_set', admin: by, provider: 'myspace' },
          { at: '2026-11-12T06:00:00Z', kind: 'provider_app_set', admin: by },
        ],
      }),
    )
    await activity()
    await log().findByText('Set up the application of Facebook')
    expect(
      log()
        .getAllByRole('listitem')
        .map((row) => row.textContent),
    ).toEqual([
      '12 Nov 2026, 09:00 UTCSet up the application of FacebookBy boss@example.org',
      '12 Nov 2026, 08:00 UTCRemoved the application of GitHubBy boss@example.org',
      "12 Nov 2026, 07:00 UTCSet up a provider's applicationBy boss@example.org",
      "12 Nov 2026, 06:00 UTCSet up a provider's applicationBy boss@example.org",
    ])
    await expectAccessible()
  })

  it('says each kind in words, and one it does not know as it is', async () => {
    asAdmin().on(
      first,
      ok({
        entries: kinds.map((kind, n) => ({
          at: `2026-11-${String(12 - Math.floor(n / 4)).padStart(2, '0')}T0${n % 10}:00:00Z`,
          kind,
          admin: by,
          ...(n === 0 ? { account: { id: 'acc_eve', address: 'eve@example.org' } } : {}),
          ...(n === 1 ? { account: { id: 'acc_gone' } } : {}),
        })),
      }),
    )
    await activity()
    await log().findByText('Blocked an account')
    expect(
      log()
        .getAllByRole('listitem')
        .map((row) => row.textContent),
    ).toEqual([
      '12 Nov 2026, 00:00 UTCBlocked an accounteve@example.orgBy boss@example.org',
      '12 Nov 2026, 01:00 UTCUnblocked an accountAn account that is gone, acc_goneBy boss@example.org',
      '12 Nov 2026, 02:00 UTCAsked to delete an accountBy boss@example.org',
      '12 Nov 2026, 03:00 UTCCancelled the deletion of an accountBy boss@example.org',
      '11 Nov 2026, 04:00 UTCSigned out every device of an accountBy boss@example.org',
      '11 Nov 2026, 05:00 UTCMade an invitationBy boss@example.org',
      '11 Nov 2026, 06:00 UTCRevoked an invitationBy boss@example.org',
      "11 Nov 2026, 07:00 UTCSet up a provider's applicationBy boss@example.org",
      "10 Nov 2026, 08:00 UTCRemoved a provider's applicationBy boss@example.org",
      '10 Nov 2026, 09:00 UTCMade an adminBy boss@example.org',
      '10 Nov 2026, 00:00 UTCRemoved an adminBy boss@example.org',
      '10 Nov 2026, 01:00 UTCsomething_newBy boss@example.org',
    ])
    expect(log().getByRole('link', { name: 'eve@example.org' }).getAttribute('href')).toBe(
      '/admin/accounts/acc_eve',
    )
    expect(log().queryByRole('button', { name: 'Show older activity' })).toBeNull()
    await expectAccessible()
  })

  it('names each invitation, also when several were made at once', async () => {
    const at = '2026-11-12T10:00:00Z'
    asAdmin().on(
      first,
      ok({
        entries: [
          { at, kind: 'invitation_revoked', admin: by, invitation: 'inv_c' },
          { at, kind: 'invitations_made', admin: by, invitation: 'inv_c' },
          { at, kind: 'invitations_made', admin: by, invitation: 'inv_b' },
          { at, kind: 'invitations_made', admin: by, invitation: 'inv_a' },
        ],
      }),
    )
    await activity()
    await log().findByText('Revoked an invitation')
    expect(
      log()
        .getAllByRole('listitem')
        .map((row) => row.textContent),
    ).toEqual([
      '12 Nov 2026, 10:00 UTCRevoked an invitationInvitation inv_cBy boss@example.org',
      '12 Nov 2026, 10:00 UTCMade an invitationInvitation inv_cBy boss@example.org',
      '12 Nov 2026, 10:00 UTCMade an invitationInvitation inv_bBy boss@example.org',
      '12 Nov 2026, 10:00 UTCMade an invitationInvitation inv_aBy boss@example.org',
    ])
    await expectAccessible()
  })

  it('pages, the newest first, and says when a page could not load', async () => {
    asAdmin()
      .on(
        first,
        ok({ entries: [{ at: '2026-11-12T10:00:00Z', kind: 'block', admin: by }], next: 'cur_2' }),
      )
      .on(`${first}&after=cur_2`, refused(500, 'internal'))
    const { user } = await activity()
    const more = await log().findByRole('button', { name: 'Show older activity' })
    await user.click(more)
    await log().findByText(
      "Older activity couldn't load. Something went wrong on our side. Try again in a moment.",
    )
    server.on(
      `${first}&after=cur_2`,
      ok({ entries: [{ at: '2026-11-01T10:00:00Z', kind: 'unblock', admin: by }] }),
    )
    await user.click(more)
    await log().findByText('Unblocked an account')
    expect(log().getAllByRole('listitem')).toHaveLength(2)
    expect(log().queryByRole('alert')).toBeNull()
    expect(log().queryByRole('button', { name: 'Show older activity' })).toBeNull()
  })

  it('says when nothing was done, and when it could not load', async () => {
    asAdmin().on(first, ok({ entries: [] }))
    await activity()
    await log().findByText('Nothing has been done here yet.')

    server.on(first, network)
    const { user } = await activity()
    const alert = await log().findByRole('alert')
    expect(alert.textContent).toContain("The activity couldn't load.")
    await expectAccessible()
    server.on(first, ok({ entries: [] }))
    await user.click(within(alert).getByRole('button', { name: 'Try again' }))
    await log().findByText('Nothing has been done here yet.')
  })
})
