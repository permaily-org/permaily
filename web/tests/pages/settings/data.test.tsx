import { screen, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { open } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { server } from '../../helpers/server.ts'

/** Opens Settings, and waits for the whole page. */
async function settings() {
  server.signedIn().settings()
  const opened = await open('/settings')
  await screen.findByText('1 device signed in')
  return opened
}

describe('notifications and your data', () => {
  it('say what is true today, and show no control that does nothing (E39)', async () => {
    await settings()
    const notifications = within(screen.getByRole('region', { name: 'Notifications' }))
    expect(
      notifications.getByText(/^Permaily emails you only about security and access/).textContent,
    ).toBe(
      "Permaily emails you only about security and access: a sign-in from a new device, a change in how you sign in or in your contact address, your account being deleted or blocked, a connection approved, renewed or given a new device, a connection that ends in a week or loses a device for safety, receipts found deleted or changed, and every device signed out by Permaily. These emails can't be turned off.",
    )
    const data = within(screen.getByRole('region', { name: 'Your data' }))
    expect(data.getByText(/^Deleting your account below/).textContent).toBe(
      "Deleting your account below stops it at once and removes it for good after 7 days: your address, your name, your ways to sign in and your devices. What your connections were given, and why, is in Activity, where you can download it. Exporting the rest of your data isn't built yet: ask us for a copy.",
    )
    expect(data.getByRole('link', { name: 'Activity' }).getAttribute('href')).toBe('/activity')
    for (const region of [notifications, data]) {
      expect(region.queryAllByRole('button')).toEqual([])
      expect(region.queryAllByRole('switch')).toEqual([])
      expect(region.queryAllByRole('checkbox')).toEqual([])
      expect(region.queryAllByRole('combobox')).toEqual([])
    }
    await expectAccessible()
  })
})
