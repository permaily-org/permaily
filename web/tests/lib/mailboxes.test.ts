import { describe, expect, it } from 'vitest'
import * as api from '../../src/lib/api.ts'
import { lost, messageOf, personal } from '../helpers/mailboxes.ts'
import { ok, refused, server } from '../helpers/server.ts'

/** The code a request fails with. */
async function failure(request: Promise<unknown>): Promise<string> {
  try {
    await request
  } catch (error) {
    expect(error).toBeInstanceOf(api.ApiError)
    return api.errorCode(error)
  }
  throw new Error('the request did not fail')
}

describe("the person's mailbox calls", () => {
  it('ask the routes of ADR 0026 with the shapes of the contract', async () => {
    server
      .on('GET /api/v1/mailboxes', ok({ mailboxes: [personal, lost], limit: 5 }))
      .on('POST /api/v1/mailboxes', ok(personal))
      .on('GET /api/v1/mailboxes/mbx%2Fodd', ok(personal))
      .on('PATCH /api/v1/mailboxes/mbx_personal', ok(personal))
      .on('DELETE /api/v1/mailboxes/mbx_personal', ok())
      .on('GET /api/v1/mailboxes/mbx_personal/messages?limit=50', ok({ messages: [] }))
      .on(
        'GET /api/v1/mailboxes/mbx_personal/messages?limit=50&after=abc&q=a+b',
        ok({ messages: [], next: 'def', searched_to: '2026-11-01T00:00:00Z' }),
      )
      .on('GET /api/v1/mailboxes/mbx_personal/messages/m1', ok(messageOf()))
      .on('DELETE /api/v1/mailboxes/mbx_personal/messages/m1', ok())
      .on('POST /api/v1/mailboxes/mbx_personal/messages/delete', ok({ deleted: 2 }))
    expect((await api.mailboxes()).mailboxes.map((m) => m.id)).toEqual(['mbx_personal', 'mbx_lost'])
    expect((await api.createMailbox('Trips')).id).toBe('mbx_personal')
    expect(server.asked('POST /api/v1/mailboxes')[0].body).toEqual({ label: 'Trips' })
    expect((await api.mailbox('mbx/odd')).id).toBe('mbx_personal')
    await api.updateMailbox('mbx_personal', { retention_days: 30 })
    expect(server.asked('PATCH /api/v1/mailboxes/mbx_personal')[0].body).toEqual({
      retention_days: 30,
    })
    await api.disconnectMailbox('mbx_personal')
    expect(await api.messages('mbx_personal')).toEqual({ messages: [] })
    expect(await api.messages('mbx_personal', { after: 'abc', q: 'a b' })).toEqual({
      messages: [],
      next: 'def',
      searched_to: '2026-11-01T00:00:00Z',
    })
    expect((await api.message('mbx_personal', 'm1')).subject).toBe('Dinner on Friday')
    await api.deleteMessage('mbx_personal', 'm1')
    expect(await api.deleteMessages('mbx_personal', ['m1', 'm2'])).toBe(2)
    expect(server.asked('POST /api/v1/mailboxes/mbx_personal/messages/delete')[0].body).toEqual({
      ids: ['m1', 'm2'],
    })
    expect(api.rawMessageUrl('mbx_personal', 'm1')).toBe(
      '/api/v1/mailboxes/mbx_personal/messages/m1/raw',
    )
    expect(api.attachmentUrl('mbx_personal', 'm1', 3)).toBe(
      '/api/v1/mailboxes/mbx_personal/messages/m1/attachments/3',
    )
  })

  it('fill what an answer leaves out, and refuse one that is not a mailbox or a message', async () => {
    server
      .on('GET /api/v1/mailboxes', ok({ mailboxes: [{ id: 'mbx_x', state: 'waiting' }] }))
      .on('GET /api/v1/mailboxes/mbx_x', ok({ id: 'mbx_x', state: 'gone' }))
      .on('GET /api/v1/mailboxes/mbx_y', ok({ state: 'waiting' }))
      .on('GET /api/v1/mailboxes/mbx_x/messages?limit=50', ok({ next: '' }))
      .on('GET /api/v1/mailboxes/mbx_x/messages?limit=50&after=n', ok({ messages: [], next: '' }))
      .on('GET /api/v1/mailboxes/mbx_x/messages/m1', ok({ id: 'm1' }))
      .on('GET /api/v1/mailboxes/mbx_x/messages/m2', ok({ subject: 'x' }))
      .on('POST /api/v1/mailboxes/mbx_x/messages/delete', ok({}))
    expect(await api.mailboxes()).toEqual({
      mailboxes: [
        {
          id: 'mbx_x',
          state: 'waiting',
          address: '',
          label: '',
          retention_options: [],
          confirmations: [],
        },
      ],
      limit: 1,
    })
    expect(await failure(api.mailbox('mbx_x'))).toBe('other')
    expect(await failure(api.mailbox('mbx_y'))).toBe('other')
    expect(await failure(api.messages('mbx_x'))).toBe('other')
    expect(await api.messages('mbx_x', { after: 'n' })).toEqual({ messages: [] })
    expect(await api.message('mbx_x', 'm1')).toEqual({
      id: 'm1',
      from: [],
      to: [],
      cc: [],
      reply_to: [],
      attachments: [],
      auth: [],
      text: '',
      subject: '',
    })
    expect(await failure(api.message('mbx_x', 'm2'))).toBe('other')
    expect(await api.deleteMessages('mbx_x', ['m1'])).toBe(0)
  })

  it('refuse a list that is not one, and pass the API’s refusals on', async () => {
    server
      .on('GET /api/v1/mailboxes', ok({ mailboxes: null }))
      .on('DELETE /api/v1/mailboxes/mbx_forked', refused(409, 'mailbox_unreadable'))
      .on('POST /api/v1/mailboxes', refused(409, 'too_many_mailboxes'))
    expect(await failure(api.mailboxes())).toBe('other')
    expect(await failure(api.disconnectMailbox('mbx_forked'))).toBe('mailbox_unreadable')
    expect(await failure(api.createMailbox(''))).toBe('too_many_mailboxes')
  })
})
