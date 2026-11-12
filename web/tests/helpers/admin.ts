import { screen, waitFor, within } from '@testing-library/react'
import { expect } from 'vitest'
import type { open } from './app.tsx'
import { type Account, ana, meOf, ok, server } from './server.ts'

type User = Awaited<ReturnType<typeof open>>['user']

/** The super admin. */
export const boss: Account = {
  ...ana,
  id: 'acc_boss',
  address: 'boss@example.org',
  name: 'Bea',
  admin: 'super',
  passkey_only: true,
}

/** An admin the super admin made. */
export const helper: Account = {
  ...ana,
  id: 'acc_helper',
  address: 'helper@example.org',
  name: 'Hugo',
  admin: 'admin',
  passkey_only: true,
}

export const stats = {
  accounts: { active: 3, closing: 1, blocked: 1, total: 5 },
  invitations: { unused: 2 },
  complete: true,
}

/** An account as the admin panel reads it. */
export function record(overrides: Record<string, unknown> = {}) {
  return {
    id: 'acc_eve',
    state: 'active',
    address: 'eve@example.org',
    name: 'Eve',
    language: 'en',
    created_at: '2026-03-06T10:00:00Z',
    address_verified_at: '2026-03-06T10:05:00Z',
    passkeys: 2,
    passkey_only: false,
    sessions: 3,
    providers: ['google', 'meta'],
    ...overrides,
  }
}

/** This admin is signed in, its account signs in with passkeys only, and it confirms by mail. */
export function asAdmin(account: Account = boss) {
  return server
    .signedIn(account)
    .on('GET /api/v1/admin/stats', ok(stats))
    .on('GET /api/v1/auth/confirm', ok({ ways: ['email'] }))
    .on('POST /api/v1/auth/confirm/email/start', ok({ attempt: 'att_confirm', expires_in: 600 }))
    .on(
      'POST /api/v1/auth/confirm/email/finish',
      ok(meOf(account, { confirmed_until: '2026-11-12T10:10:00Z' })),
    )
}

/** What the dialog says of why it asks, for an admin action: not that it decides who gets into the account. */
export const adminReason =
  "This is an admin action, so Permaily asks you to show it's you again. It counts for the next 10 minutes."

/** Confirms with the code of the message, in the dialog that asks for it, which says it is an admin action. */
export async function confirm(user: User) {
  const dialog = within(await screen.findByRole('dialog', { name: "Confirm it's you" }))
  expect(dialog.getByText(adminReason)).toBeTruthy()
  await user.click(await dialog.findByRole('button', { name: 'Email me a code' }))
  await user.type(await dialog.findByLabelText('Code from the message'), '123456')
  await user.click(dialog.getByRole('button', { name: 'Confirm' }))
  await waitFor(() => expect(screen.queryByRole('dialog', { name: "Confirm it's you" })).toBeNull())
}

/** Cancels the dialog that asks the person to confirm. */
export async function cancelConfirm(user: User) {
  const dialog = within(await screen.findByRole('dialog', { name: "Confirm it's you" }))
  expect(dialog.getByText(adminReason)).toBeTruthy()
  await user.click(await dialog.findByRole('button', { name: 'Cancel' }))
  await waitFor(() => expect(screen.queryByRole('dialog', { name: "Confirm it's you" })).toBeNull())
}

/** Every request the app made of the admin API. */
export function adminRequests(): string[] {
  return server.calls
    .filter((call) => call.path.startsWith('/api/v1/admin'))
    .map((call) => `${call.method} ${call.path}`)
}
