import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { asAdmin, boss, cancelConfirm, confirm, helper } from '../../helpers/admin.ts'
import { expectNotKept, named, open } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { confirmationNeeded, network, ok, once, refused, server } from '../../helpers/server.ts'

const list = 'GET /api/v1/admin/providers'
const secret = 'typed-only-in-this-test'

async function providers(as = boss) {
  asAdmin(as)
  const opened = await open('/admin/providers')
  await screen.findByRole('heading', { level: 1, name: 'Providers' })
  return opened
}

function rows() {
  return within(screen.getByRole('region', { name: 'Sign-in providers' }))
}

describe('providers', () => {
  it('shows which are set up, Meta as Facebook, and lets the super admin change them', async () => {
    server.on(
      list,
      ok({
        providers: [
          { provider: 'google', client_id: 'g-client-1' },
          { provider: 'meta', client_id: 'fb-1' },
          { provider: 'nope' },
        ],
      }),
    )
    await providers()
    await rows().findByText('Client ID g-client-1')
    const text = screen.getByRole('region', { name: 'Sign-in providers' }).textContent
    expect(text).toContain('GoogleSet up')
    expect(text).toContain('FacebookSet up')
    expect(text).toContain('GitHubNot set up')
    expect(text).toContain('MicrosoftNot set up')
    expect(text).not.toContain('Meta')
    expect(rows().getByRole('button', { name: named('Change Google') })).toBeTruthy()
    expect(rows().getByRole('button', { name: named('Remove Facebook') })).toBeTruthy()
    expect(rows().getByRole('button', { name: named('Set up GitHub') })).toBeTruthy()
    expect(rows().queryByRole('button', { name: named('Remove GitHub') })).toBeNull()
    await expectAccessible()
  })

  it('offers an admin who is not the super admin nothing to change', async () => {
    server.on(list, ok({ providers: [{ provider: 'google', client_id: 'g-client-1' }] }))
    await providers(helper)
    await rows().findByText('Client ID g-client-1')
    expect(rows().getByText('Only the super admin can set up providers.')).toBeTruthy()
    expect(rows().queryAllByRole('button')).toEqual([])
  })

  it('says when the list could not load', async () => {
    server.on(list, network)
    const { user } = await providers()
    const alert = await rows().findByRole('alert')
    expect(alert.textContent).toContain("The providers couldn't load.")
    server.on(list, ok({ providers: [] }))
    await user.click(within(alert).getByRole('button', { name: 'Try again' }))
    await rows().findByRole('button', { name: named('Set up GitHub') })
  })

  it('sets one up: the secret is a password field, sent once and then gone from everywhere', async () => {
    server
      .on(list, ok({ providers: [] }))
      .on('PUT /api/v1/admin/providers/github', once(confirmationNeeded, ok()))
    const { user } = await providers()
    await user.click(await rows().findByRole('button', { name: named('Set up GitHub') }))
    const form = within(screen.getByRole('dialog', { name: 'Set up GitHub' }))
    const id = form.getByLabelText('Client ID') as HTMLInputElement
    const field = form.getByLabelText('Client secret') as HTMLInputElement
    expect(field.type).toBe('password')
    expect(field.autocomplete).toBe('off')
    await waitFor(() => expect(document.activeElement).toBe(id))
    await expectAccessible()

    await user.click(form.getByRole('button', { name: 'Save' }))
    await form.findByText('Enter the client ID.')
    await user.type(id, ' gh-client ')
    await user.click(form.getByRole('button', { name: 'Save' }))
    await form.findByText('Enter the client secret.')
    expect(document.activeElement).toBe(field)

    await user.type(field, secret)
    server.on(list, ok({ providers: [{ provider: 'github', client_id: 'gh-client' }] }))
    await user.click(form.getByRole('button', { name: 'Save' }))
    expect(field.value).toBe('')
    await confirm(user)
    await rows().findByText('GitHub set up.')
    expect(server.asked('PUT /api/v1/admin/providers/github').map((one) => one.body)).toEqual([
      { client_id: 'gh-client', client_secret: secret },
      { client_id: 'gh-client', client_secret: secret },
    ])
    expect(screen.queryByRole('dialog', { name: 'Set up GitHub' })).toBeNull()
    expect(document.body.innerHTML).not.toContain(secret)
    expectNotKept(secret)
    await rows().findByText('Client ID gh-client')
  })

  it('clears the secret when the server refuses it too, and says to enter it again', async () => {
    server
      .on(list, ok({ providers: [{ provider: 'google', client_id: 'g-client-1' }] }))
      .on('PUT /api/v1/admin/providers/google', refused(400, 'invalid_provider_app'))
    const { user } = await providers()
    await user.click(await rows().findByRole('button', { name: named('Change Google') }))
    const form = within(screen.getByRole('dialog', { name: 'Change Google' }))
    expect((form.getByLabelText('Client ID') as HTMLInputElement).value).toBe('g-client-1')
    const field = form.getByLabelText('Client secret') as HTMLInputElement
    await user.type(field, secret)
    await user.click(form.getByRole('button', { name: 'Save' }))
    await form.findByText(
      "Permaily couldn't use that client ID and secret. Check them, then enter the secret again.",
    )
    expect(field.value).toBe('')
    await waitFor(() => expect(document.activeElement).toBe(field))
    expect(document.body.innerHTML).not.toContain(secret)

    server.on('PUT /api/v1/admin/providers/google', refused(403, 'super_admin_only'))
    await user.type(field, secret)
    await user.click(form.getByRole('button', { name: 'Save' }))
    await form.findByText('Nothing was changed. Only the super admin can set up providers.')
    expect(field.value).toBe('')

    // Not confirming leaves the form as it was, with nothing more said.
    server.on('PUT /api/v1/admin/providers/google', confirmationNeeded)
    await user.type(field, secret)
    await user.click(form.getByRole('button', { name: 'Save' }))
    await cancelConfirm(user)
    expect(field.value).toBe('')
    await user.click(form.getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Change Google' })).toBeNull())
    expectNotKept(secret)
  })

  it('removes one, asking first', async () => {
    server
      .on(list, ok({ providers: [{ provider: 'meta', client_id: 'fb-1' }] }))
      .on('DELETE /api/v1/admin/providers/meta', once(refused(403, 'super_admin_only'), ok()))
    const { user } = await providers()
    await user.click(await rows().findByRole('button', { name: named('Remove Facebook') }))
    const question = within(screen.getByRole('dialog', { name: 'Remove Facebook?' }))
    expect(
      question.getByText('Nobody can sign in with Facebook, or link it, until it is set up again.'),
    ).toBeTruthy()
    await expectAccessible()
    await user.click(question.getByRole('button', { name: 'Remove Facebook' }))
    await question.findByText('Only the super admin can set up providers.')
    server.on(list, ok({ providers: [] }))
    await user.click(question.getByRole('button', { name: 'Remove Facebook' }))
    await rows().findByText('Facebook removed.')
    await rows().findByRole('button', { name: named('Set up Facebook') })
  })
})
