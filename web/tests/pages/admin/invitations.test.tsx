import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { asAdmin, cancelConfirm, confirm } from '../../helpers/admin.ts'
import { expectNotKept, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import {
  confirmationNeeded,
  later,
  network,
  ok,
  once,
  refused,
  server,
} from '../../helpers/server.ts'

const list = 'GET /api/v1/admin/invitations'
const waiting = [
  { id: 'inv_1', created_at: '2026-11-10T09:00:00Z', expires_at: '2026-12-10T09:00:00Z' },
  { id: 'inv_2', created_at: '2026-11-11T15:30:00Z', expires_at: '2026-12-11T15:30:00Z' },
]
const codes = ['ABCD-EFGH-JKLM', 'NPQR-STUV-WXYZ']

async function invitations() {
  const opened = await open('/admin/invitations')
  await screen.findByRole('heading', { level: 1, name: 'Invitations' })
  return opened
}

function unused() {
  return within(screen.getByRole('region', { name: 'Not used yet' }))
}

function making() {
  return within(screen.getByRole('region', { name: 'Make invitations' }))
}

describe('invitations', () => {
  it('lists the ones not used yet, and never their codes', async () => {
    asAdmin().on(list, ok({ invitations: waiting, truncated: true }))
    await invitations()
    await unused().findByText('Made 10 Nov 2026, 09:00 UTC')
    expect(unused().getByText('Works until 10 Dec 2026')).toBeTruthy()
    expect(unused().getByText('Not every unused invitation is listed here.')).toBeTruthy()
    expect(
      unused()
        .getAllByRole('button')
        .map((one) => one.textContent),
    ).toEqual([
      'Revoke invitation made 10 Nov 2026, 09:00 UTC',
      'Revoke invitation made 11 Nov 2026, 15:30 UTC',
    ])
    await expectAccessible()
  })

  it('says when none waits, and when the list could not load', async () => {
    asAdmin().on(list, ok({ invitations: [], truncated: false }))
    await invitations()
    await unused().findByText('No invitation is waiting to be used.')

    server.on(list, network)
    const { user } = await invitations()
    const alert = await unused().findByRole('alert')
    expect(alert.textContent).toContain("The invitations couldn't load.")
    await expectAccessible()
    server.on(list, ok({ invitations: waiting, truncated: false }))
    await user.click(within(alert).getByRole('button', { name: 'Try again' }))
    await unused().findByText('Made 10 Nov 2026, 09:00 UTC')
  })

  it('makes 1 to 20 at a time, and says so before asking', async () => {
    asAdmin().on(list, ok({ invitations: [], truncated: false }))
    const { user } = await invitations()
    const count = making().getByLabelText('How many')
    for (const typed of ['0', '21', 'x']) {
      await user.clear(count)
      if (typed !== 'x') await user.type(count, typed)
      await user.click(making().getByRole('button', { name: 'Make invitations' }))
      await making().findByText('Make 1 to 20 at a time.')
      expect(document.activeElement).toBe(count)
    }
    await settle()
    expect(server.asked('POST /api/v1/admin/invitations')).toEqual([])
  })

  it('shows the codes once, to copy, and keeps them nowhere', async () => {
    const writeText = vi.fn(async () => {})
    asAdmin()
      .on(list, ok({ invitations: [], truncated: false }))
      .on(
        'POST /api/v1/admin/invitations',
        once(
          confirmationNeeded,
          ok({ invitations: codes, ids: ['inv_3', 'inv_4'], expires_at: '2026-12-12T10:00:00Z' }),
        ),
      )
    const { user } = await invitations()
    // After the page opened: the library that plays the person brings a clipboard of its own.
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText } })
    const count = making().getByLabelText('How many')
    await user.clear(count)
    await user.type(count, '2')
    server.on(list, ok({ invitations: waiting, truncated: false }))
    await user.click(making().getByRole('button', { name: 'Make invitations' }))
    await confirm(user)
    const shown = within(await screen.findByRole('dialog', { name: 'Your 2 new invitations' }))
    expect(server.asked('POST /api/v1/admin/invitations').map((one) => one.body)).toEqual([
      { count: 2 },
      { count: 2 },
    ])
    expect(shown.getByText("These codes won't be shown again.").parentElement?.textContent).toBe(
      "These codes won't be shown again. Copy them now. Each one makes one account, until 12 Dec 2026.",
    )
    expect(shown.getAllByRole('listitem').map((one) => one.textContent)).toEqual(codes)
    await waitFor(() =>
      expect(document.activeElement).toBe(
        shown.getByRole('heading', { name: 'Your 2 new invitations' }),
      ),
    )
    await expectAccessible()
    // Closing or reloading the tab asks first.
    const leaving = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(leaving)
    expect(leaving.defaultPrevented).toBe(true)
    // Escape leaves it open.
    screen
      .getByRole('dialog', { name: 'Your 2 new invitations' })
      .dispatchEvent(new Event('cancel', { cancelable: true }))
    expect(screen.getByRole('dialog', { name: 'Your 2 new invitations' })).toBeTruthy()

    await user.click(shown.getByRole('button', { name: 'Copy all' }))
    expect(writeText).toHaveBeenCalledWith('ABCD-EFGH-JKLM\nNPQR-STUV-WXYZ')
    await shown.findByText('Copied 2 invitations.')
    expectNotKept(...codes)
    await user.click(shown.getByRole('button', { name: 'Done' }))
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Your 2 new invitations' })).toBeNull(),
    )
    expect(document.body.textContent).not.toContain(codes[0])
    await unused().findByText('Made 10 Nov 2026, 09:00 UTC')
    await waitFor(() => expect(server.asked('GET /api/v1/admin/stats')).toHaveLength(2))
    Reflect.deleteProperty(navigator, 'clipboard')
  })

  it('says when the codes could not be copied, and names one code as one', async () => {
    asAdmin()
      .on(list, ok({ invitations: [], truncated: false }))
      .on(
        'POST /api/v1/admin/invitations',
        ok({ invitations: [codes[0]], ids: ['inv_3'], expires_at: '' }),
      )
    const { user } = await invitations()
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: { writeText: async () => Promise.reject(new Error('denied')) },
    })
    await user.click(making().getByRole('button', { name: 'Make invitations' }))
    const shown = within(await screen.findByRole('dialog', { name: 'Your new invitation' }))
    expect(shown.getByText("This code won't be shown again.").parentElement?.textContent).toBe(
      "This code won't be shown again. Copy it now. Each one makes one account.",
    )
    await user.click(shown.getByRole('button', { name: 'Copy all' }))
    await shown.findByText("Couldn't copy. Select the codes and copy them by hand.")
    Reflect.deleteProperty(navigator, 'clipboard')
  })

  it('says when none were made, and nothing when the person does not confirm', async () => {
    asAdmin()
      .on(list, ok({ invitations: [], truncated: false }))
      .on('POST /api/v1/admin/invitations', refused(429, 'too_many_requests'))
    const { user } = await invitations()
    await user.click(making().getByRole('button', { name: 'Make invitations' }))
    await making().findByText(
      'No invitations were made. Too many attempts. Wait a few minutes, then try again.',
    )
    server.on('POST /api/v1/admin/invitations', confirmationNeeded)
    await user.click(making().getByRole('button', { name: 'Make invitations' }))
    await cancelConfirm(user)
    expect(
      making()
        .getAllByRole('alert')
        .map((one) => one.textContent)
        .join(''),
    ).toBe('')
  })

  it('revokes one, asking first', async () => {
    asAdmin()
      .on(list, ok({ invitations: waiting, truncated: false }))
      .on('DELETE /api/v1/admin/invitations/inv_1', once(confirmationNeeded, ok()))
    const { user } = await invitations()
    await user.click(
      await unused().findByRole('button', { name: /^Revoke\s*invitation made 10 Nov/ }),
    )
    const question = within(screen.getByRole('dialog', { name: 'Revoke this invitation?' }))
    expect(question.getByText('Made 10 Nov 2026, 09:00 UTC')).toBeTruthy()
    expect(
      question.getByText('Whoever has its code can no longer make an account with it.'),
    ).toBeTruthy()
    await expectAccessible()
    server.on(list, ok({ invitations: [waiting[1]], truncated: false }))
    await user.click(question.getByRole('button', { name: 'Revoke invitation' }))
    await confirm(user)
    await unused().findByText('Invitation revoked.')
    await waitFor(() => expect(unused().queryByText('Made 10 Nov 2026, 09:00 UTC')).toBeNull())
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('region', { name: 'Not used yet' })),
    )
  })

  it('says when an invitation was used, revoked or never made, and reads the list again', async () => {
    const reading = later()
    asAdmin()
      .on(list, ok({ invitations: waiting, truncated: false }))
      .on('DELETE /api/v1/admin/invitations/inv_1', refused(404, 'not_found'))
    const { user } = await invitations()
    await user.click(
      await unused().findByRole('button', { name: /^Revoke\s*invitation made 10 Nov/ }),
    )
    server.on(list, reading.answer)
    const question = within(screen.getByRole('dialog', { name: 'Revoke this invitation?' }))
    await user.click(question.getByRole('button', { name: 'Revoke invitation' }))
    await question.findByText('That invitation was used, revoked or never made.')
    reading.give(ok({ invitations: [waiting[1]], truncated: false }))
    await waitFor(() => expect(server.asked(list)).toHaveLength(2))
  })
})
