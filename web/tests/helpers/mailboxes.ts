/**
 * Mailboxes and messages as the server answers them (ADR 0026, "The API"), with made-up addresses only. A test
 * changes what it needs.
 */

/** The address of the first sample mailbox: made up, at the inbox's host. */
export const personalAddress = 'q7wz3kfn9tr2hmx4vp8c@inbox.permaily.com'
export const workAddress = 'b4ne8sjd2ug6yatc5rkm@inbox.permaily.com'

export function mailboxOf(overrides: Record<string, unknown> = {}) {
  return {
    id: 'mbx_personal',
    address: personalAddress,
    label: 'Personal',
    state: 'receiving',
    created_at: '2026-11-02T09:00:00Z',
    retention_days: 90,
    retention_options: [7, 30, 90, 180, 365],
    receiving_since: '2026-11-03T08:15:00Z',
    confirmations: [],
    ...overrides,
  }
}

export const personal = mailboxOf()

export const work = mailboxOf({
  id: 'mbx_work',
  address: workAddress,
  label: 'Work',
  state: 'waiting',
  receiving_since: undefined,
})

/** A mailbox whose record can't be read: only its id and when it was added. */
export const lost = {
  id: 'mbx_lost',
  address: '',
  label: '',
  state: 'unreadable',
  created_at: '2026-10-01T09:00:00Z',
  retention_days: 0,
  retention_options: [],
  confirmations: [],
}

/** A confirmation Gmail sent: Google's check is off, so it is not verified. */
export function confirmationOf(overrides: Record<string, unknown> = {}) {
  return {
    code: '836104527',
    requester: 'ana.ruiz@example.com',
    received_at: '2026-11-12T10:04:00Z',
    verified: false,
    message: 'b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6',
    ...overrides,
  }
}

/** A message in a list. Identifiers are 32 hex characters, time first. */
export function summaryOf(n: number, overrides: Record<string, unknown> = {}) {
  return {
    id: `0000019a${String(n).padStart(24, '0')}`,
    received_at: `2025-10-${String(10 + (n % 18)).padStart(2, '0')}T09:30:00Z`,
    expires_at: '2026-01-10T09:30:00Z',
    size: 4200,
    from: [{ name: `Sender ${n}`, address: `sender${n}@example.com` }],
    subject: `Subject ${n}`,
    snippet: `The start of message ${n}`,
    attachments: 0,
    confirmation: false,
    ...overrides,
  }
}

export function messageOf(overrides: Record<string, unknown> = {}) {
  return {
    id: summaryOf(1).id,
    received_at: '2025-10-11T09:30:00Z',
    expires_at: '2026-01-09T09:30:00Z',
    size: 18_400,
    from: [{ name: 'Ana Ruiz', address: 'ana.ruiz@example.com' }],
    to: [{ address: personalAddress }],
    cc: [],
    reply_to: [],
    subject: 'Dinner on Friday',
    date: 'Sat, 11 Oct 2025 11:30:00 +0200',
    text: 'Hi,\n\nAre we still on for Friday?\n\nAna',
    text_from: 'plain',
    text_truncated: false,
    attachments: [],
    auth: [{ method: 'dkim', domain: 'example.com', result: 'pass' }],
    confirmation: false,
    unparsed: false,
    ...overrides,
  }
}

/** Mail is hostile input: this must show as these characters, never become an element. */
export const hostile = '<img src=x onerror=alert(1)>'
