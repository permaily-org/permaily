import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { adminRequests, asAdmin, boss, helper, stats } from '../../helpers/admin.ts'
import { current, named, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { ana, meOf, network, ok, refused, server } from '../../helpers/server.ts'

function sidebar() {
  return within(screen.getByRole('navigation', { name: 'Main' }))
}

describe('the admin panel is hidden from anyone who is not an admin', () => {
  it.each([
    '/admin',
    '/admin/accounts',
    '/admin/accounts/acc_eve',
    '/admin/providers',
    '/admin/nothing',
  ])(
    '%s leads where any unknown route leads, without asking the server anything',
    async (address) => {
      server.signedIn()
      await open(address)
      await waitFor(() => expect(current()).toBe('/'))
      await settle()
      expect(adminRequests()).toEqual([])
      expect(sidebar().queryByRole('link', { name: 'Admin' })).toBeNull()
    },
  )
})

describe('the admin panel', () => {
  it('is in the sidebar of an admin, and opens on the accounts with the numbers on top', async () => {
    asAdmin().on('GET /api/v1/admin/accounts?limit=50', ok({ accounts: [], unreadable: 0 }))
    const { user } = await open('/')
    await user.click(
      await within(await screen.findByRole('navigation', { name: 'Main' })).findByRole('link', {
        name: 'Admin',
      }),
    )
    await waitFor(() => expect(current()).toBe('/admin/accounts'))
    const heading = await screen.findByRole('heading', { level: 1, name: 'Accounts' })
    await waitFor(() => expect(document.activeElement).toBe(heading))
    const numbers = within(screen.getByRole('region', { name: 'Numbers' }))
    expect(numbers.getByText('Accounts').nextElementSibling?.textContent).toBe('5')
    expect(numbers.getByText('Being deleted').nextElementSibling?.textContent).toBe('1')
    expect(numbers.getByText('Unused invitations').nextElementSibling?.textContent).toBe('2')
    expect(screen.queryByText(/at least what it shows/)).toBeNull()
    const tabs = within(screen.getByRole('navigation', { name: 'Admin' }))
    expect(tabs.getAllByRole('link').map((link) => link.textContent)).toEqual([
      'Accounts',
      'Invitations',
      'Providers',
      'Admins',
      'Activity',
    ])
    await expectAccessible()
  })

  it('says when the numbers are only a floor, and when they could not load', async () => {
    asAdmin()
      .on('GET /api/v1/admin/stats', ok({ ...stats, complete: false }))
      .on('GET /api/v1/admin/accounts?limit=50', ok({ accounts: [], unreadable: 1 }))
    await open('/admin/accounts')
    await screen.findByText(
      "Some accounts couldn't be read, so each number is at least what it shows.",
    )
    expect(screen.getAllByText('At least')).toHaveLength(5)

    server.on('GET /api/v1/admin/stats', network)
    const { user } = await open('/admin/accounts')
    const alert = await screen.findByText(/The numbers couldn't load\./)
    expect(alert.textContent).toBe(
      "The numbers couldn't load. Permaily can't be reached. Check your connection, then try again.",
    )
    // The rest of the panel works all the same.
    await screen.findByText('There are no accounts that could be read.')
    await expectAccessible()
    server.on('GET /api/v1/admin/stats', ok(stats))
    await user.click(
      within(alert.parentElement as HTMLElement).getByRole('button', { name: 'Try again' }),
    )
    await screen.findByRole('region', { name: 'Numbers' })
  })

  it('works only while the account signs in with passkeys only, and says why', async () => {
    asAdmin().on('GET /api/v1/admin/stats', refused(403, 'passkey_only_needed'))
    const { user } = await open('/admin/accounts')
    await screen.findByRole('heading', { name: 'Turn on passkey only to use the admin panel' })
    expect(
      screen.getByText('The admin panel works only for accounts that sign in with passkeys only.'),
    ).toBeTruthy()
    expect(
      screen.getByText(
        'While a message to your email address can sign you in, anyone who reads your mail could sign in as you and act as an admin. With passkey only on, only your passkeys sign you in.',
      ),
    ).toBeTruthy()
    // Nothing else of the panel was asked.
    expect(adminRequests()).toEqual(['GET /api/v1/admin/stats'])
    await expectAccessible()

    server.settings({ passkeys: [{ id: 'cred-1', name: 'Phone' }] })
    await user.click(screen.getByRole('link', { name: 'Go to sign-in settings' }))
    const section = await screen.findByRole('region', { name: 'Sign-in' })
    await screen.findByText('1 device signed in')
    await waitFor(() => expect(document.activeElement).toBe(section))
    expect(current()).toBe('/settings')
  })

  it('shows that screen from what the session says, asking the panel nothing', async () => {
    asAdmin({ ...boss, passkey_only: false })
    await open('/admin/accounts')
    await screen.findByRole('heading', { name: 'Turn on passkey only to use the admin panel' })
    await settle()
    expect(adminRequests()).toEqual([])
  })

  it('opens once passkey only is turned on in Settings, without a reload', async () => {
    asAdmin({ ...boss, passkey_only: false })
      .settings({ passkeys: [{ id: 'cred-1', name: 'Phone' }] })
      .on('GET /api/v1/admin/accounts?limit=50', ok({ accounts: [], unreadable: 0 }))
      .on('POST /api/v1/account/passkey-only', () => {
        server.settings({ passkeys: [{ id: 'cred-1', name: 'Phone' }], passkeyOnly: true })
        return ok({ passkey_only: true, recovery_codes: ['AAAA-BBBB-CCCC-DDDD'] })
      })
    const { user } = await open('/admin/accounts')
    await user.click(await screen.findByRole('link', { name: 'Go to sign-in settings' }))
    const section = within(await screen.findByRole('region', { name: 'Sign-in' }))
    await user.click(await section.findByRole('button', { name: named('Turn on passkey only') }))
    await user.click(
      within(screen.getByRole('dialog', { name: 'Sign in with passkeys only?' })).getByRole(
        'button',
        { name: 'Turn on passkey only' },
      ),
    )
    const shown = within(await screen.findByRole('dialog', { name: 'Keep your recovery codes' }))
    await user.click(shown.getByRole('checkbox'))
    await user.click(shown.getByRole('button', { name: 'Done' }))
    await user.click(sidebar().getByRole('link', { name: 'Admin' }))
    await screen.findByRole('heading', { level: 1, name: 'Accounts' })
    expect(screen.queryByRole('heading', { name: /Turn on passkey only/ })).toBeNull()
  })

  it('shuts when passkey only was turned off in another tab', async () => {
    asAdmin().on('GET /api/v1/admin/accounts?limit=50', ok({ accounts: [], unreadable: 0 }))
    await open('/admin/accounts')
    await screen.findByRole('heading', { level: 1, name: 'Accounts' })
    server.on('GET /api/v1/me', ok(meOf({ ...boss, passkey_only: false })))
    window.dispatchEvent(new Event('focus'))
    await screen.findByRole('heading', { name: 'Turn on passkey only to use the admin panel' })
  })

  it('shows the same screen when passkey only is turned off while the panel is open', async () => {
    asAdmin().on('GET /api/v1/admin/accounts?limit=50', refused(403, 'passkey_only_needed'))
    await open('/admin/accounts')
    await screen.findByRole('heading', { name: 'Turn on passkey only to use the admin panel' })
  })

  it('leads to the sign-in screen when the session ended', async () => {
    asAdmin().on('GET /api/v1/admin/accounts?limit=50', () => {
      server.signedOut()
      return refused(401, 'unauthorized')
    })
    await open('/admin/accounts')
    await waitFor(() => expect(current()).toBe('/sign-in'))
    expect(screen.queryByText(/couldn't load/)).toBeNull()
  })

  it('goes when the account is no longer an admin', async () => {
    asAdmin().on('GET /api/v1/admin/accounts?limit=50', () => {
      server.signedIn(ana)
      return refused(404, 'not_found')
    })
    await open('/admin/accounts')
    await waitFor(() => expect(current()).toBe('/'))
    expect(sidebar().queryByRole('link', { name: 'Admin' })).toBeNull()
  })

  it('appears when the account becomes an admin in another tab', async () => {
    server.signedIn(ana)
    await open('/')
    await screen.findByRole('navigation', { name: 'Main' })
    expect(sidebar().queryByRole('link', { name: 'Admin' })).toBeNull()
    server.on('GET /api/v1/me', ok(meOf({ ...ana, admin: 'admin' })))
    window.dispatchEvent(new Event('focus'))
    await sidebar().findByRole('link', { name: 'Admin' })
  })

  it('leads an unknown page of the panel where any unknown route leads', async () => {
    asAdmin(helper)
    await open('/admin/nothing')
    await waitFor(() => expect(current()).toBe('/'))
    expect(sidebar().getByRole('link', { name: 'Admin' })).toBeTruthy()
  })
})
