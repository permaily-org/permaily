import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { named, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { signedInWithMailboxes, tripApp } from '../../helpers/connections.ts'
import { ana, meOf, ok, refused, server } from '../../helpers/server.ts'

// K55 (ADR 0039, pull request 5): a connection's page shows its webhook: whom it tells and when, its state, the next
// run in the person's time zone, when a notice was last taken, why calls fail in fixed words, and the button that
// turns it off with its words; turning it off on a stale page shows the connection again. Against the contract and a
// played server.

const weekly = { every: 'week', on: 'monday', at: '08:00' }

/** Trip App's connection with a webhook told of new mail and every Monday at 08:00 in Madrid. */
function withWebhook(
  webhook: Record<string, unknown> = {},
  overrides: Record<string, unknown> = {},
) {
  return {
    ...tripApp,
    webhook: {
      host: 'trips.example',
      new_mail: true,
      schedule: weekly,
      time_zone: 'Europe/Madrid',
      state: 'on',
      // Monday 23 November 2026 at 08:00 in Madrid.
      next_at: '2026-11-23T07:00:00Z',
      last_sent_at: '2026-11-16T07:00:00Z',
      ...webhook,
    },
    ...overrides,
  }
}

const offRoute = 'POST /api/v1/connections/con_tripapp/webhook/off'

async function openConnection(c: Record<string, unknown>) {
  signedInWithMailboxes()
    .on('GET /api/v1/connections', ok({ connections: [c] }))
    .on('GET /api/v1/connections/con_tripapp', ok(c))
  const opened = await open('/connections/trips.example/con_tripapp')
  await screen.findByRole('heading', { level: 1, name: /reads your mail\.$/ })
  return opened
}

function section() {
  return within(screen.getByRole('region', { name: 'Tells the app' }))
}

/** The text of a row of the section, by its label. */
function row(label: string): string {
  return section().getByText(label).parentElement?.textContent ?? ''
}

describe('K55: the webhook on the connection’s page', () => {
  it('shows whom it tells and when, its state, the next run in the person’s zone and the last notice taken', async () => {
    await openConnection(withWebhook())
    expect(row('Calls')).toContain('trips.example')
    expect(row('When')).toContain('Tells trips.example when new mail it may read arrives')
    expect(row('When')).toContain(
      'Tells trips.example every Monday at 08:00, your time (Europe/Madrid)',
    )
    expect(row('When')).toContain('is told only that there is mail to fetch, never the mail')
    expect(row('State')).toContain('On')
    // In Madrid's time, not the browser's (UTC in the tests).
    expect(row('Next')).toContain('Mon 23 Nov, 08:00')
    expect(row('Next')).toContain('(Europe/Madrid)')
    expect(row('Last told')).toContain('Mon 16 Nov, 08:00')
    expect(section().queryByText(/Calls have failed/)).toBeNull()
    expect(
      section().getByRole('button', { name: named('Turn off telling trips.example') }),
    ).toBeTruthy()
    await expectAccessible()
  })

  it('says since when and why calls fail, in fixed words', async () => {
    for (const [failure, words] of [
      ['no_answer', "trips.example didn't answer"],
      ['error', 'trips.example answered with an error'],
      ['redirect', "trips.example answered with a redirect, which Permaily doesn't follow"],
      ['refused', "Permaily couldn't make a secure connection to trips.example"],
      ['something_new', 'trips.example answered with an error'],
    ]) {
      server.reset()
      await openConnection(
        withWebhook({ failing_since: '2026-11-20T09:30:00Z', failure, last_sent_at: undefined }),
      )
      const notice = section().getByText('Calls have failed since Fri 20 Nov, 10:30.').parentElement
      expect(notice?.textContent).toContain(`${words}.`)
      expect(notice?.textContent).toContain('Permaily keeps trying for 72 hours for each notice.')
      expect(row('Last told')).toContain('Not yet')
    }
    await expectAccessible()
  })

  it('a webhook told only of new mail has no next run, and one in no zone reads in the browser’s', async () => {
    await openConnection(
      withWebhook({ schedule: undefined, next_at: undefined, time_zone: undefined }),
    )
    expect(row('When')).toContain('when new mail it may read arrives')
    expect(row('When')).not.toContain('your time')
    expect(section().queryByText('Next')).toBeNull()
    expect(row('Last told')).toContain('Mon 16 Nov, 07:00')
  })

  it('reads a zone the browser does not know in the browser’s own', async () => {
    await openConnection(withWebhook({ time_zone: 'Mars/Olympus' }))
    expect(row('Next')).toContain('Mon 23 Nov, 07:00')
    expect(row('Next')).toContain('(Mars/Olympus)')
  })

  it('says a webhook turned off or stopped, offers no button, and shows nothing of a connection without one', async () => {
    await openConnection(withWebhook({ state: 'off', next_at: undefined }))
    expect(row('State')).toContain('Turned off by you')
    expect(section().queryByRole('button')).toBeNull()
    expect(section().queryByText('Next')).toBeNull()

    server.reset()
    await openConnection(withWebhook({ state: 'stopped' }))
    expect(row('State')).toContain('Stopped: trips.example asked not to be told any more')
    expect(section().queryByRole('button')).toBeNull()
    // A stopped webhook's failures and next run are not news any more.
    expect(section().queryByText('Next')).toBeNull()

    // A connection that ended: its webhook ended with it, and nothing turns it off.
    server.reset()
    signedInWithMailboxes()
    const gone = withWebhook(
      {},
      { state: 'ended', ended_at: '2026-11-18T10:00:00Z', end_reason: 'person' },
    )
    server
      .on('GET /api/v1/connections', ok({ connections: [gone] }))
      .on('GET /api/v1/connections/con_tripapp', ok(gone))
    await open('/connections/trips.example/con_tripapp')
    await screen.findByRole('heading', { level: 1, name: 'Trip App no longer reads your mail.' })
    expect(row('State')).toContain('Ended with the connection')
    expect(section().queryByRole('button')).toBeNull()
    expect(section().queryByText('Next')).toBeNull()

    server.reset()
    await openConnection(tripApp)
    expect(screen.queryByRole('region', { name: 'Tells the app' })).toBeNull()
  })

  it('turns it off after asking, at the version shown, with no fresh proof, and says so', async () => {
    const off = withWebhook({ state: 'off', next_at: undefined }, { version: 3 })
    server.on(offRoute, ok(off))
    const { user } = await openConnection(withWebhook())
    await user.click(
      section().getByRole('button', { name: named('Turn off telling trips.example') }),
    )
    const dialog = within(
      await screen.findByRole('dialog', { name: 'Stop telling trips.example?' }),
    )
    expect(
      dialog.getByText(
        /is no longer told when there's mail to fetch\. This is final for this connection: only a new request from the app turns it on again\. It can still read your mail when it asks\./,
      ),
    ).toBeTruthy()
    await expectAccessible()
    await user.click(dialog.getByRole('button', { name: 'Turn off' }))
    await screen.findByText('Turned off. trips.example is no longer told.')
    expect(server.asked(offRoute).map((a) => a.body)).toEqual([{ version: 2 }])
    expect(row('State')).toContain('Turned off by you')
    expect(section().queryByRole('button')).toBeNull()
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('heading', { name: 'Tells the app' })),
    )
    // The connection keeps reading: turning the webhook off revokes nothing.
    expect(screen.getByRole('button', { name: named('Revoke Trip App') })).toBeTruthy()
  })

  it('cancelling turns nothing off', async () => {
    const { user } = await openConnection(withWebhook())
    await user.click(
      section().getByRole('button', { name: named('Turn off telling trips.example') }),
    )
    const dialog = within(
      await screen.findByRole('dialog', { name: 'Stop telling trips.example?' }),
    )
    await user.click(dialog.getByRole('button', { name: 'Cancel' }))
    await settle()
    expect(server.asked(offRoute)).toHaveLength(0)
  })

  it('on a stale page, says so and shows the connection again', async () => {
    server.on(offRoute, refused(409, 'consent_changed'))
    const { user } = await openConnection(withWebhook())
    // Meanwhile, the vendor stopped it.
    server.on(
      'GET /api/v1/connections/con_tripapp',
      ok(withWebhook({ state: 'stopped' }, { version: 3 })),
    )
    await user.click(
      section().getByRole('button', { name: named('Turn off telling trips.example') }),
    )
    const dialog = within(
      await screen.findByRole('dialog', { name: 'Stop telling trips.example?' }),
    )
    await user.click(dialog.getByRole('button', { name: 'Turn off' }))
    await screen.findByText(
      'What allowing does changed since this page opened. Read it again, then decide.',
    )
    await waitFor(() =>
      expect(row('State')).toContain('Stopped: trips.example asked not to be told any more'),
    )
    expect(server.asked('GET /api/v1/connections/con_tripapp')).toHaveLength(2)
  })

  it('turns it off under terms the account has not agreed to, as revoking works', async () => {
    const off = withWebhook({ state: 'off', next_at: undefined })
    server.on(offRoute, ok(off))
    signedInWithMailboxes()
      .on('GET /api/v1/me', ok(meOf(ana, {}, '2027-01-01')))
      .on('GET /api/v1/connections', ok({ connections: [withWebhook()] }))
      .on('GET /api/v1/connections/con_tripapp', ok(withWebhook()))
    const { user } = await open('/connections/trips.example/con_tripapp')
    await screen.findByRole('heading', { level: 1, name: /reads your mail\.$/ })
    // The limited shell: no renewing, but revoking and turning the webhook off.
    expect(screen.queryByRole('button', { name: named('Renew Trip App') })).toBeNull()
    await user.click(
      section().getByRole('button', { name: named('Turn off telling trips.example') }),
    )
    const dialog = within(
      await screen.findByRole('dialog', { name: 'Stop telling trips.example?' }),
    )
    await user.click(dialog.getByRole('button', { name: 'Turn off' }))
    await screen.findByText('Turned off. trips.example is no longer told.')
  })
})
