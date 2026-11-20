import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { open } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import {
  defaultRule,
  previewOf,
  previewRoute,
  requestOf,
  signedInWithMailboxes,
  startRoute,
  summary,
  tripApp,
  vendorQuery,
} from '../../helpers/connections.ts'
import { personal } from '../../helpers/mailboxes.ts'
import { bankRule, checkOf, ruleListOf, ruleOf, ruleSentence } from '../../helpers/rules.ts'
import { ok, refused, server } from '../../helpers/server.ts'

// U32 (ADR 0040, "Previews and consent"): the rules that will apply are on the consent screen, each by the server's
// sentence with what it does and who for, and its preview with the model says what would leave with details taken
// out; a connection's page lists the rules that apply to it, the default first, then the person's that are on and
// whose scope covers its app, matched exactly by its document.

const home = ruleOf()
const a = 'https://vendor.example/client-a.json'
const b = 'https://vendor.example/client-b.json'
/** A rule for Client A only, on the same host as Client B. */
const onlyA = ruleOf(
  { id: 'rul_a' },
  checkOf(
    {
      draft_id: 'rdr_a',
      sentence: ruleSentence('From school.example'),
      does: 'Holds the email back',
      for: 'Only vendor.example/client-a.json',
      model: false,
    },
    { name: 'School', effect: 'hold', removal: undefined, scope: { to: 'only', clients: [a] } },
  ),
)
const offRule = ruleOf(
  { id: 'rul_off' },
  checkOf(
    { draft_id: 'rdr_off', sentence: ruleSentence('From off.example') },
    { name: 'Off', on: false },
  ),
)

function summaryOf(rule: ReturnType<typeof ruleOf>) {
  return { id: rule.id, version: rule.version, sentence: rule.check.sentence.formulas }
}

describe('U32: the consent screen lists the rules that will apply', () => {
  it('says each by its sentence, what it does and who for, and what would leave with details taken out', async () => {
    const request = requestOf({ rules: [defaultRule, summaryOf(home), summaryOf(bankRule)] })
    signedInWithMailboxes()
      .on('GET /api/v1/rules', ok(ruleListOf([home, ruleOf({ id: 'rul_bank', version: 9 })])))
      .on(startRoute, ok({ request }))
      .on(previewRoute, ok(previewOf()))
    await open(`/oauth/authorize?${vendorQuery}`)
    await screen.findByRole('heading', { name: /asks to read your mail\.$/ })
    const rules = within(screen.getByRole('region', { name: 'Your rules apply first' }))
    await rules.findByText(home.check.does)
    expect(rules.getByText('Home address')).toBeTruthy()
    expect(rules.getByText('The message mentions “a home address”')).toBeTruthy()
    expect(rules.getAllByText('Every connection').length).toBeGreaterThan(0)
    expect(rules.getByText('Sign-in codes and password resets')).toBeTruthy()
    expect(rules.getByText('Holds the email back')).toBeTruthy()
    // A rule the list has at another version is said by the server's sentence alone, and no rule leaves the screen.
    expect(rules.getByText('From bank.example')).toBeTruthy()
    expect(rules.queryByText('Only trips.example/client.json')).toBeNull()
    expect(rules.queryAllByRole('link')).toHaveLength(0)
    expect(screen.getByRole('main').textContent).toContain(
      "claude.ai isn't told what they hold back, and sees “[removed]” where they took details out.",
    )
    await expectAccessible()
  })

  it('says only the sentences when the account’s rules can’t be read', async () => {
    signedInWithMailboxes()
      .on('GET /api/v1/rules', refused(503, 'other'))
      .on(startRoute, ok({ request: requestOf({ rules: [defaultRule, summaryOf(home)] }) }))
      .on(previewRoute, ok(previewOf()))
    await open(`/oauth/authorize?${vendorQuery}`)
    const rules = within(await screen.findByRole('region', { name: 'Your rules apply first' }))
    await waitFor(() => expect(server.asked('GET /api/v1/rules')).toHaveLength(1))
    expect(rules.getByText('The message mentions “a home address”')).toBeTruthy()
    expect(rules.queryByText(home.check.does)).toBeNull()
  })

  it('says what each does and who for in the server’s own words, the account’s rules read or not (U25)', async () => {
    const request = requestOf({
      rules: [
        {
          ...defaultRule,
          does: 'Holds the email back',
          for: 'Every connection, those made later included',
        },
        {
          ...summaryOf(onlyA),
          does: 'Takes out phone numbers, and the sender and attachments, which it does not read',
          for: 'Only the connections of vendor.example/client-a.json',
          clients: [{ client_id: a, shown: 'vendor.example/client-a.json' }],
        },
      ],
    })
    signedInWithMailboxes()
      .on('GET /api/v1/rules', refused(503, 'other'))
      .on(startRoute, ok({ request }))
      .on(previewRoute, ok(previewOf()))
    await open(`/oauth/authorize?${vendorQuery}`)
    const rules = within(await screen.findByRole('region', { name: 'Your rules apply first' }))
    await waitFor(() => expect(server.asked('GET /api/v1/rules')).toHaveLength(1))
    expect(rules.getByText('Every connection, those made later included')).toBeTruthy()
    expect(
      rules.getByText(
        'Takes out phone numbers, and the sender and attachments, which it does not read',
      ),
    ).toBeTruthy()
    expect(rules.getByText('Only the connections of vendor.example/client-a.json')).toBeTruthy()
    expect(rules.queryByText('Only vendor.example/client-a.json')).toBeNull()
  })

  it('counts, with the model, what would leave with details your rules take out', async () => {
    const page = (removed?: string[]) => {
      const preview = previewOf([summary(1), summary(2)], { model: 'asked' })
      return { ...preview, page: { ...preview.page, ...(removed ? { removed } : {}) } }
    }
    signedInWithMailboxes()
      .on('GET /api/v1/rules', ok(ruleListOf([home])))
      .on(
        startRoute,
        ok({ request: requestOf({ draft: { mailboxes: [personal.id], since: { new: false } } }) }),
      )
      .on(previewRoute, (asked) =>
        ok((asked.body as { ask_model?: boolean }).ask_model ? page([summary(1).id]) : page()),
      )
    const { user } = await open(`/oauth/authorize?${vendorQuery}`)
    await user.click(await screen.findByRole('button', { name: 'Preview with the model' }))
    await screen.findByText('1 of them leave with details your rules take out.')
  })
})

describe('U32: a connection’s page lists the rules that apply to it', () => {
  async function openConnection(c: Record<string, unknown>, rules: unknown) {
    signedInWithMailboxes()
      .on('GET /api/v1/rules', ok(rules))
      .on('GET /api/v1/connections', ok({ connections: [c] }))
      .on(`GET /api/v1/connections/${c.id as string}`, ok(c))
    await open(`/connections/${(c.vendor as { domain: string }).domain}/${c.id as string}`)
    await screen.findByRole('heading', { level: 1, name: /reads your mail\.$/ })
    return within(screen.getByRole('region', { name: 'Your rules apply first' }))
  }

  it('the default first, then the rules on whose scope covers its app, holds before removals, each opening its page', async () => {
    const rules = await openConnection(tripApp, ruleListOf([home, offRule, bankRule]))
    await rules.findByText('Bank')
    const items = rules.getAllByRole('listitem').map((li) => li.textContent ?? '')
    expect(items).toHaveLength(3)
    expect(items[0]).toContain('Sign-in codes and password resets')
    expect(items[1]).toContain('Bank')
    expect(items[2]).toContain('Home address')
    expect(rules.queryByText('Off')).toBeNull()
    expect(rules.getByRole('link', { name: 'Bank' }).getAttribute('href')).toBe('/rules/rul_bank')
    await expectAccessible()
  })

  it('tells two apps on one host apart: a rule for one does not apply to the other', async () => {
    const clientB = { ...tripApp, id: 'con_b', vendor: { domain: 'vendor.example' }, client_id: b }
    const rules = await openConnection(clientB, ruleListOf([onlyA], { on: false }))
    await rules.findByRole('list')
    expect(rules.queryByText('School')).toBeNull()
    expect(rules.queryAllByRole('listitem')).toHaveLength(0)
    const clientA = { ...clientB, id: 'con_a', client_id: a }
    const forA = await openConnection(clientA, ruleListOf([onlyA], { on: false }))
    await forA.findByText('School')
  })

  it('says when the rules could not load, and tries again', async () => {
    signedInWithMailboxes()
      .on('GET /api/v1/rules', refused(500, 'other'))
      .on('GET /api/v1/connections', ok({ connections: [tripApp] }))
      .on('GET /api/v1/connections/con_tripapp', ok(tripApp))
    const { user } = await open('/connections/trips.example/con_tripapp')
    const section = within(await screen.findByRole('region', { name: 'Your rules apply first' }))
    await section.findByText(/Your rules couldn't load/)
    server.on('GET /api/v1/rules', ok(ruleListOf([])))
    await user.click(section.getByRole('button', { name: 'Try again' }))
    await section.findByText('Sign-in codes and password resets')
  })
})
