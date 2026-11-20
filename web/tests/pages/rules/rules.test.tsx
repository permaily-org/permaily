import { screen, waitFor, within } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { RULES_NOTICE_VERSION, rulesNotice } from '../../../src/lib/rules-notice.ts'
import {
  inFlight,
  senderAndAttachments,
  shapeWords,
  topicsOverRemove,
} from '../../../src/pages/rules/words.ts'
import rule from '../../formula/rule.json'
import { current, open, settle } from '../../helpers/app.tsx'
import { expectAccessible } from '../../helpers/axe.ts'
import { connectionOf, noRules, summary, tripApp } from '../../helpers/connections.ts'
import { personal, work } from '../../helpers/mailboxes.ts'
import {
  bankRule,
  checkOf,
  formulaOf,
  ruleListOf,
  ruleOf,
  ruleSentence,
} from '../../helpers/rules.ts'
import {
  type Asked,
  ana,
  confirmationNeeded,
  meOf,
  ok,
  once,
  refused,
  server,
} from '../../helpers/server.ts'

// U32 (ADR 0040, pull request 7): the rules screens against a played server. The list, the default's switch, the
// person's rules and their switches, the rules' notice, words to a proposal, the editor (criteria, what it does, who
// for), the sentence the server wrote and saving it, deleting, the preview; every change asked first, saying that an
// answer already being prepared can finish, and made with a fresh proof.

type User = Awaited<ReturnType<typeof open>>['user']

const home = ruleOf()
const list = 'GET /api/v1/rules'

/** The account's rules, its mailboxes and its connections, as the rules screens read them. */
function rulesServer(rules = ruleListOf([home, bankRule])) {
  return server
    .on('GET /api/v1/me', ok(meOf(ana)))
    .on(list, ok(rules))
    .on('GET /api/v1/mailboxes', ok({ mailboxes: [personal, work], limit: 5 }))
    .on('GET /api/v1/connections', ok({ connections: [] }))
}

/** The server's check of whatever draft is sent: the draft as it came, with an identifier of its own. */
function echoCheck(words: Record<string, unknown> = {}) {
  return (asked: Asked) => {
    const draft = (asked.body as { draft: Record<string, unknown> }).draft
    const effect = draft.effect as string
    return ok(
      checkOf(
        {
          draft_id: `rdr_${JSON.stringify(draft).length}_${String(draft.on)}`,
          does: effect === 'hold' ? 'Holds the email back' : 'Takes out what you chose',
          for: 'Every connection',
          model:
            JSON.stringify(draft).includes('"about"') || JSON.stringify(draft).includes('topics'),
          ...words,
        },
        draft,
      ),
    )
  }
}

/** Gives the fresh proof the dialog asks for, with the code of the message. */
function confirming() {
  return server
    .on('GET /api/v1/auth/confirm', ok({ ways: ['email'] }))
    .on('POST /api/v1/auth/confirm/email/start', ok({ attempt: 'att_c', expires_in: 600 }))
    .on(
      'POST /api/v1/auth/confirm/email/finish',
      ok(meOf(ana, { confirmed_until: '2026-11-20T12:10:00Z' })),
    )
}

/** Confirms it's them in the dialog, which says why: rules decide what leaves, and an answer in flight can finish. */
async function confirm(user: User) {
  const dialog = within(await screen.findByRole('dialog', { name: "Confirm it's you" }))
  expect(dialog.getByText(/Rules decide what leaves your mailboxes/).textContent).toContain(
    inFlight,
  )
  await user.click(await dialog.findByRole('button', { name: 'Email me a code' }))
  await user.type(await dialog.findByLabelText('Code from the message'), '123456')
  await user.click(dialog.getByRole('button', { name: 'Confirm' }))
}

describe('U32: the rules page', () => {
  it('lists the default rule and the person’s, each by what it does and who for, in the server’s words', async () => {
    rulesServer(ruleListOf([home, ruleOf({ id: 'rul_old', model_off: true })]))
    await open('/rules')
    await screen.findByRole('heading', { level: 1, name: '2 rules of your own.' })
    const own = within(screen.getByRole('list', { name: 'Your rules' }))
    const link = own.getAllByRole('link')[0]
    expect(link.getAttribute('href')).toBe('/rules/rul_home')
    expect(link.textContent).toContain('Home address')
    expect(link.textContent).toContain(home.check.does)
    expect(link.textContent).toContain('Every connection')
    expect(own.getByText('Asks the model under an older notice')).toBeTruthy()
    expect(screen.getByText('Sign-in codes and password resets')).toBeTruthy()
    expect(
      screen.getByRole('switch', { name: 'Sign-in and security' }).getAttribute('aria-checked'),
    ).toBe('true')
    expect(screen.getByRole('link', { name: 'New rule' }).getAttribute('href')).toBe('/rules/new')
    // The notice is agreed: nothing asks for it.
    expect(screen.queryByText('Read and agree')).toBeNull()
    await expectAccessible()
  })

  it('says when there is none of the person’s own, and when there are as many as an account can have', async () => {
    rulesServer(ruleListOf([]))
    await open('/rules')
    await screen.findByRole('heading', { level: 1, name: 'No rules of your own yet.' })
    rulesServer(ruleListOf([home], { max: 1 }))
    await open('/rules')
    await screen.findByRole('heading', { level: 1, name: '1 rule of your own.' })
    const button = screen.getByRole('button', { name: 'New rule' })
    expect(button.getAttribute('aria-disabled')).toBe('true')
    expect(
      screen.getByText('You have 1 rules, as many as an account can. Delete one to make another.'),
    ).toBeTruthy()
  })

  it('says when the rules could not load, and tries again', async () => {
    rulesServer().on(list, once(refused(500, 'other'), ok(ruleListOf([]))))
    const { user } = await open('/rules')
    await screen.findByText(/Your rules couldn't load/)
    await user.click(screen.getByRole('button', { name: 'Try again' }))
    await screen.findByRole('heading', { level: 1, name: 'No rules of your own yet.' })
  })

  it('turns the default rule off after asking, saying what it lets out and that an answer in flight can finish', async () => {
    rulesServer().on(
      'POST /api/v1/rules/default',
      once(confirmationNeeded, ok({ rule: noRules.default.rule, on: false })),
    )
    confirming()
    const { user } = await open('/rules')
    await user.click(await screen.findByRole('switch', { name: 'Sign-in and security' }))
    const dialog = within(
      await screen.findByRole('dialog', { name: 'Turn the Sign-in and security rule off?' }),
    )
    expect(dialog.getByText(/can then leave to every connection/)).toBeTruthy()
    expect(dialog.getByText(inFlight)).toBeTruthy()
    server.on(list, ok(ruleListOf([home, bankRule], { on: false, version: 4 })))
    await user.click(dialog.getByRole('button', { name: 'Turn off' }))
    await confirm(user)
    await screen.findByText('The Sign-in and security rule is off.')
    const asked = server.asked('POST /api/v1/rules/default')
    expect(asked).toHaveLength(2)
    expect(asked[1].body).toEqual({ on: false, version: 3 })
    await waitFor(() =>
      expect(
        screen.getByRole('switch', { name: 'Sign-in and security' }).getAttribute('aria-checked'),
      ).toBe('false'),
    )
    await waitFor(() =>
      expect(document.activeElement).toBe(screen.getByRole('heading', { level: 1 })),
    )
  })

  it('cancelling the question changes nothing', async () => {
    rulesServer()
    const { user } = await open('/rules')
    await user.click(await screen.findByRole('switch', { name: 'Sign-in and security' }))
    const dialog = within(await screen.findByRole('dialog'))
    await user.click(dialog.getByRole('button', { name: 'Cancel' }))
    await settle()
    expect(server.asked('POST /api/v1/rules/default')).toHaveLength(0)
  })

  it('turns a rule off by saving its draft with the switch off, checked first, at the version read', async () => {
    rulesServer()
      .on('POST /api/v1/rules/check', echoCheck())
      .on('PUT /api/v1/rules/rul_bank', ok(bankRule))
    const { user } = await open('/rules')
    await user.click(await screen.findByRole('switch', { name: 'Bank' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Turn Bank off?' }))
    expect(dialog.getByText(inFlight)).toBeTruthy()
    await user.click(dialog.getByRole('button', { name: 'Turn off' }))
    await screen.findByText('Bank is off.')
    const checked = server.asked('POST /api/v1/rules/check')[0].body as { draft: { on: boolean } }
    expect(checked.draft.on).toBe(false)
    const saved = server.asked('PUT /api/v1/rules/rul_bank')[0].body as Record<string, unknown>
    expect(saved.version).toBe(3)
    expect(saved.draft_id).toBe(`rdr_${JSON.stringify(checked.draft).length}_false`)
    // A rule no model decides is saved without the notice.
    expect(saved.notice).toBeUndefined()
    expect(server.asked(list).length).toBeGreaterThanOrEqual(2)
  })

  it('says when the rules changed in another window, and reads them again', async () => {
    rulesServer()
      .on('POST /api/v1/rules/check', echoCheck())
      .on('PUT /api/v1/rules/rul_home', refused(409, 'rules_changed'))
    const { user } = await open('/rules')
    await user.click(await screen.findByRole('switch', { name: 'Home address' }))
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Turn off' }),
    )
    await screen.findByText('Your rules changed in another window. They are shown as they are now.')
    await waitFor(() => expect(server.asked(list)).toHaveLength(2))
    const saved = server.asked('PUT /api/v1/rules/rul_home')[0].body as { notice?: string }
    expect(saved.notice).toBe(RULES_NOTICE_VERSION)
  })

  it('asks for the rules’ notice when it is not agreed, and agrees with a fresh proof', async () => {
    rulesServer(ruleListOf([home], { notice: null })).on(
      'POST /api/v1/account/rules-notice',
      once(confirmationNeeded, ok({ version: RULES_NOTICE_VERSION, at: '2026-11-20T12:00:00Z' })),
    )
    confirming()
    const { user } = await open('/rules')
    await user.click(await screen.findByRole('button', { name: 'Read and agree' }))
    for (const paragraph of rulesNotice) expect(screen.getByText(paragraph)).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'Agree' }))
    await confirm(user)
    await screen.findByText('Agreed. Rules a model decides can ask it now.')
    expect(server.asked('POST /api/v1/account/rules-notice')[1].body).toEqual({
      version: RULES_NOTICE_VERSION,
    })
    expect(screen.queryByRole('button', { name: 'Read and agree' })).toBeNull()
    await expectAccessible()
  })
})

describe('U32: making a rule', () => {
  it('turns words into a proposal, shows the server’s sentence, and saves exactly it after asking', async () => {
    const proposed = checkOf({ draft_id: 'rdr_proposed' })
    rulesServer(ruleListOf([]))
      .on('POST /api/v1/rules/words', ok({ check: proposed, model: 'gpt-6-luna', prompt: 'w1' }))
      .on('POST /api/v1/rules/check', echoCheck())
      .on('POST /api/v1/rules', ok(home))
    const { user } = await open('/rules/new')
    await screen.findByRole('heading', { name: 'What you save' })
    await user.type(
      screen.getByRole('textbox', { name: /Only these words go to the model/ }),
      'Take my home address out of everything',
    )
    await user.click(screen.getByRole('button', { name: 'Turn into a rule' }))
    await screen.findByText(/Proposed: the fields below are the model’s/)
    expect(server.asked('POST /api/v1/rules/words')[0].body).toEqual({
      words: 'Take my home address out of everything',
      notice: RULES_NOTICE_VERSION,
    })
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('Home address')
    // The proposal's criteria are in the editor's terms: the message mentions "a home address".
    const condition = within(screen.getByRole('group', { name: 'The message mentions' }))
    expect(condition.getByText('a home address')).toBeTruthy()
    // What it takes out, each kind with what it takes out besides.
    expect(
      (screen.getByRole('checkbox', { name: /^Phone numbers/ }) as HTMLInputElement).checked,
    ).toBe(true)
    expect(screen.getByText(shapeWords.phone_number.also)).toBeTruthy()
    expect(screen.getByText(topicsOverRemove)).toBeTruthy()
    expect(screen.getByText(senderAndAttachments)).toBeTruthy()
    // The sentence the server wrote is what Save confirms.
    const saving = within(screen.getByRole('region', { name: 'What you save' }))
    await saving.findByText('The message mentions “a home address”')
    expect(saving.getByText('Takes out what you chose')).toBeTruthy()

    await user.click(screen.getByRole('button', { name: 'Save rule' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Save this rule?' }))
    expect(dialog.getByText('The message mentions “a home address”')).toBeTruthy()
    expect(dialog.getByText(/^Patterns take out more than they name/)).toBeTruthy()
    expect(dialog.getByText(topicsOverRemove)).toBeTruthy()
    expect(dialog.getByText(senderAndAttachments)).toBeTruthy()
    expect(dialog.getByText(inFlight)).toBeTruthy()
    await user.click(dialog.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(current()).toBe('/rules'))
    const [save] = server.asked('POST /api/v1/rules')
    const lastCheck = server.asked('POST /api/v1/rules/check').at(-1)?.body as { draft: unknown }
    expect(save?.body).toEqual({
      draft_id: `rdr_${JSON.stringify(lastCheck.draft).length}_true`,
      draft: lastCheck.draft,
      version: 3,
      notice: RULES_NOTICE_VERSION,
    })
    await screen.findByText(`Home address saved. ${inFlight}`)
  })

  it('without the rules’ notice agreed, shows it where the words go, and sends nothing', async () => {
    rulesServer(ruleListOf([], { notice: null }))
      .on('POST /api/v1/rules/check', echoCheck())
      .on(
        'POST /api/v1/account/rules-notice',
        ok({ version: RULES_NOTICE_VERSION, at: '2026-11-20T12:00:00Z' }),
      )
    const { user } = await open('/rules/new')
    await screen.findByRole('heading', { name: 'What you save' })
    expect(screen.queryByRole('button', { name: 'Turn into a rule' })).toBeNull()
    for (const paragraph of rulesNotice) expect(screen.getByText(paragraph)).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'Agree' }))
    await screen.findByRole('button', { name: 'Turn into a rule' })
    expect(server.asked('POST /api/v1/rules/words')).toHaveLength(0)
    await expectAccessible()
  })

  it('says the model can’t be asked, and that the rule is built from the conditions instead', async () => {
    rulesServer(ruleListOf([]))
      .on('POST /api/v1/rules/check', echoCheck())
      .on('POST /api/v1/rules/words', refused(503, 'model_unavailable'))
    const { user } = await open('/rules/new')
    await user.type(
      await screen.findByRole('textbox', { name: /Only these words go to the model/ }),
      'no bank mail',
    )
    await user.click(screen.getByRole('button', { name: 'Turn into a rule' }))
    await screen.findByText(/Permaily can't ask its model now/)
    expect(screen.getByRole('textbox', { name: /Permaily can’t ask its model now/ })).toBeTruthy()
    server.on('POST /api/v1/rules/words', refused(422, 'rule_words_unusable'))
    await user.click(screen.getByRole('button', { name: 'Turn into a rule' }))
    await screen.findByText(/Permaily couldn't turn that into a rule/)
  })

  it('builds a rule from conditions, says why the server refuses one, and picks apps by their document', async () => {
    const a = connectionOf({
      id: 'con_a',
      vendor: { domain: 'vendor.example' },
      client_id: 'https://vendor.example/client-a.json',
      client_name: 'Client A',
    })
    const b = connectionOf({
      id: 'con_b',
      vendor: { domain: 'vendor.example' },
      client_id: 'https://vendor.example/client-b.json',
      client_name: 'Client B',
    })
    rulesServer(ruleListOf([]))
      .on('GET /api/v1/connections', ok({ connections: [a, b, tripApp] }))
      .on('POST /api/v1/rules/check', (asked) => {
        const draft = (asked.body as { draft: { scope: { to: string; clients?: string[] } } }).draft
        if (draft.scope.to !== 'all' && !draft.scope.clients?.length) {
          return {
            status: 400,
            body: {
              error: { code: 'invalid_rule', message: 'x', details: { rule: 'scope_clients' } },
            },
          }
        }
        return echoCheck()(asked)
      })
    const { user } = await open('/rules/new')
    await screen.findByRole('heading', { name: 'What you save' })
    await user.click(screen.getByRole('button', { name: 'Add criteria' }))
    const criteria = within(
      screen.getByRole('group', { name: 'Criteria 1, where every condition holds' }),
    )
    await user.selectOptions(criteria.getByRole('combobox', { name: 'Field' }), 'from_domain')
    await user.selectOptions(criteria.getByRole('combobox', { name: 'Compares' }), 'not:is')
    await user.type(
      criteria.getByRole('textbox', { name: "Add to Sender domain isn't" }),
      'bank.example{Enter}',
    )
    expect(criteria.getByText('bank.example')).toBeTruthy()

    await user.click(screen.getByRole('radio', { name: 'Only the apps you pick' }))
    // Two apps on one host are two apps, each by its document; the name it gives itself is said as its own.
    const appA = screen.getByRole('checkbox', { name: /vendor\.example\/client-a\.json/ })
    expect(appA.closest('label')?.textContent).toContain('calls itself Client A')
    expect(screen.getByRole('checkbox', { name: /vendor\.example\/client-b\.json/ })).toBeTruthy()
    await screen.findByText('Pick 1 to 16 apps, or choose every connection.')
    await user.click(appA)
    const saving = within(screen.getByRole('region', { name: 'What you save' }))
    await saving.findByText('Holds the email back')
    const sent = server.asked('POST /api/v1/rules/check').at(-1)?.body as {
      draft: Record<string, unknown>
    }
    expect(sent.draft).toEqual({
      name: '',
      // web/tests/formula's Go test parses this formula with internal/query: the two cannot drift.
      criteria: [rule],
      effect: 'hold',
      scope: { to: 'only', clients: ['https://vendor.example/client-a.json'] },
      on: true,
    })
    await expectAccessible()
  })

  it('takes out kinds of data and topics, each with what it takes out besides', async () => {
    rulesServer(ruleListOf([])).on('POST /api/v1/rules/check', echoCheck())
    const { user } = await open('/rules/new')
    await screen.findByRole('heading', { name: 'What you save' })
    await user.click(screen.getByRole('radio', { name: /^Take details out/ }))
    for (const kind of Object.values(shapeWords)) {
      expect(screen.getByRole('checkbox', { name: new RegExp(`^${kind.label}`) })).toBeTruthy()
      expect(screen.getByText(kind.also)).toBeTruthy()
    }
    expect(screen.queryByText(senderAndAttachments)).toBeNull()
    await user.click(screen.getByRole('checkbox', { name: /^Codes/ }))
    expect(screen.getByText(senderAndAttachments)).toBeTruthy()
    await user.click(screen.getByRole('checkbox', { name: 'The subject' }))
    await user.type(screen.getByLabelText(/^Topics, sentence by sentence/), 'my health{Enter}')
    expect(screen.getByText('my health')).toBeTruthy()
    await waitFor(() => {
      const sent = server.asked('POST /api/v1/rules/check').at(-1)?.body as {
        draft: { removal?: unknown }
      }
      expect(sent.draft.removal).toEqual({
        parts: ['subject'],
        shapes: ['numeric_code'],
        topics: ['my health'],
      })
    })
    await user.click(screen.getByRole('button', { name: 'Remove topic my health' }))
    expect(screen.queryByText('my health')).toBeNull()
  })
})

describe('U32: the criteria editor', () => {
  it('edits values, operators, mailboxes and criteria, and keeps a group it can’t show until removed', async () => {
    const mixed = {
      v: 1,
      all: [
        {
          any: [
            { var: 'subject', op: 'contains', value: 'x' },
            { var: 'body', op: 'contains', value: 'y' },
          ],
        },
      ],
    }
    const proposal = checkOf(
      {
        draft_id: 'rdr_two',
        sentence: {
          lang: 'en',
          text: 'Any of these',
          lead: [{ text: 'Any of these:' }],
          formulas: [
            {
              lang: 'en',
              text: 'The subject contains x, or the text contains y',
              lead: [],
              lines: [{ parts: [{ text: 'The subject contains x, or the text contains y' }] }],
            },
            ruleSentence('Has attachments').formulas[0],
          ],
        },
      },
      {
        name: 'Two',
        effect: 'hold',
        removal: undefined,
        criteria: [mixed, formulaOf('attachments', 'exists')],
      },
    )
    rulesServer(ruleListOf([]))
      .on('POST /api/v1/rules/check', (asked) =>
        (asked.body as { draft: { name: string } }).draft.name === 'Two'
          ? ok(proposal)
          : echoCheck()(asked),
      )
      .on('POST /api/v1/rules/words', ok({ check: proposal, model: 'm', prompt: 'p' }))
    const { user } = await open('/rules/new')
    await user.type(
      await screen.findByRole('textbox', { name: /Only these words go to the model/ }),
      'two things',
    )
    await user.click(screen.getByRole('button', { name: 'Turn into a rule' }))
    // The group of two fields is said by the server's line; the sentence of several criteria leads with its words.
    const first = within(
      await screen.findByRole('group', { name: 'Criteria 1, where every condition holds' }),
    )
    expect(first.getByText('The subject contains x, or the text contains y')).toBeTruthy()
    const saving = within(screen.getByRole('region', { name: 'What you save' }))
    await saving.findByText('Any of these:')
    const second = within(
      screen.getByRole('group', { name: 'Criteria 2, where every condition holds' }),
    )
    expect(second.getByRole('group', { name: 'Attachments there are some' })).toBeTruthy()
    await user.selectOptions(second.getByRole('combobox', { name: 'Compares' }), 'not:exists')
    expect(second.getByRole('group', { name: 'Attachments there are none' })).toBeTruthy()

    // Removing the kept group leaves criteria with no condition, which matches all mail.
    await user.click(first.getByRole('button', { name: 'Remove condition' }))
    expect(first.getByText('All mail')).toBeTruthy()
    await user.click(first.getByRole('button', { name: /^And/ }))
    const row = within(first.getByRole('group', { name: 'The message is about' }))
    await user.selectOptions(row.getByRole('combobox', { name: 'Compares' }), 'not:mentions')
    await user.type(
      row.getByRole('textbox', { name: "Add to The message doesn't mention" }),
      'my health',
    )
    await user.click(row.getByRole('button', { name: "Add to The message doesn't mention" }))
    expect(row.getByText('(a model decides)')).toBeTruthy()
    await user.click(
      row.getByRole('button', { name: "Remove my health from The message doesn't mention" }),
    )
    expect(row.queryByText('my health')).toBeNull()
    await user.selectOptions(row.getByRole('combobox', { name: 'Field' }), 'mailbox')
    const mailbox = within(first.getByRole('group', { name: 'Mailbox is' }))
    await user.selectOptions(mailbox.getByRole('combobox', { name: 'Add to Mailbox is' }), work.id)
    expect(mailbox.getByText('Work', { selector: 'bdi' })).toBeTruthy()
    await user.click(mailbox.getByRole('button', { name: 'Remove condition: Mailbox is' }))
    await user.click(screen.getByRole('button', { name: 'Remove criteria 2' }))
    expect(
      screen.queryByRole('group', { name: 'Criteria 2, where every condition holds' }),
    ).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Or: add criteria' }))
    expect(
      screen.getByRole('group', { name: 'Criteria 2, where every condition holds' }),
    ).toBeTruthy()
    await waitFor(() => {
      const sent = server.asked('POST /api/v1/rules/check').at(-1)?.body as {
        draft: { criteria: unknown }
      }
      expect(sent.draft.criteria).toEqual([
        { v: 1, all: [] },
        { v: 1, all: [] },
      ])
    })
  })
})

describe('U32: a rule of the person’s', () => {
  it('opens with its fields, says when it asks the model under an older notice, and deletes after asking', async () => {
    const old = ruleOf({ model_off: true })
    rulesServer(ruleListOf([old, bankRule]))
      .on('POST /api/v1/rules/check', echoCheck())
      .on('POST /api/v1/rules/rul_home/delete', once(confirmationNeeded, ok()))
    confirming()
    const { user } = await open('/rules/rul_home')
    await screen.findByRole('heading', { level: 1, name: 'Home address' })
    expect(screen.getByText('This rule asks the model under an older notice.')).toBeTruthy()
    expect(
      (screen.getByRole('radio', { name: /^Take details out/ }) as HTMLInputElement).checked,
    ).toBe(true)
    expect(
      (screen.getByRole('radio', { name: /^Every connection, those/ }) as HTMLInputElement).checked,
    ).toBe(true)
    await user.click(screen.getByRole('button', { name: 'Delete rule' }))
    const dialog = within(await screen.findByRole('dialog', { name: 'Delete Home address?' }))
    expect(dialog.getByText(inFlight)).toBeTruthy()
    server.on(list, ok(ruleListOf([bankRule], { version: 4 })))
    await user.click(dialog.getByRole('button', { name: 'Delete' }))
    await confirm(user)
    await waitFor(() => expect(current()).toBe('/rules'))
    expect(server.asked('POST /api/v1/rules/rul_home/delete')[1].body).toEqual({ version: 3 })
    await screen.findByText(`Home address deleted. ${inFlight}`)
  })

  it('keeps the person’s choice of who it’s for when words propose another rule', async () => {
    rulesServer()
      .on('POST /api/v1/rules/check', echoCheck())
      .on(
        'POST /api/v1/rules/words',
        ok({
          check: checkOf({}, { name: 'Other', scope: { to: 'all' } }),
          model: 'm',
          prompt: 'p',
        }),
      )
    const { user } = await open('/rules/rul_bank')
    await screen.findByRole('heading', { level: 1, name: 'Bank' })
    expect(
      (screen.getByRole('radio', { name: 'Only the apps you pick' }) as HTMLInputElement).checked,
    ).toBe(true)
    await user.type(screen.getByRole('textbox', { name: /Only these words/ }), 'home address')
    await user.click(screen.getByRole('button', { name: 'Turn into a rule' }))
    await screen.findByText(/Proposed/)
    expect(
      (screen.getByRole('radio', { name: 'Only the apps you pick' }) as HTMLInputElement).checked,
    ).toBe(true)
    expect((screen.getByLabelText('Name') as HTMLInputElement).value).toBe('Bank')
  })

  it('says what the server refuses when saving, and shows a rule changed in another window', async () => {
    rulesServer()
      .on('POST /api/v1/rules/check', echoCheck())
      .on('PUT /api/v1/rules/rul_bank', refused(409, 'rule_changed'))
    const { user } = await open('/rules/rul_bank')
    await screen.findByRole('heading', { level: 1, name: 'Bank' })
    const saving = within(screen.getByRole('region', { name: 'What you save' }))
    await saving.findByText('Holds the email back')
    const save = screen.getByRole('button', { name: 'Save rule' })
    await user.click(save)
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Save' }),
    )
    await screen.findByText(
      'That rule changed since it was checked. Read its sentence again, then save.',
    )

    for (const [code, words] of [['too_many_rules', /as many rules as an account can/]] as const) {
      server.on('PUT /api/v1/rules/rul_bank', refused(409, code))
      await user.type(screen.getByLabelText('Name'), '!')
      await saving.findByText('Holds the email back')
      await waitFor(() =>
        expect(
          screen.getByRole('button', { name: 'Save rule' }).getAttribute('aria-disabled'),
        ).toBe('false'),
      )
      await user.click(screen.getByRole('button', { name: 'Save rule' }))
      await user.click(
        within(await screen.findByRole('dialog')).getByRole('button', { name: 'Save' }),
      )
      await screen.findByText(words)
    }

    const elsewhere = ruleOf(
      { id: 'rul_bank' },
      checkOf(
        {
          draft_id: 'rdr_elsewhere',
          sentence: ruleSentence('From bank.example or bank.test'),
          does: 'Holds the email back',
          for: 'Every connection',
          model: false,
        },
        {
          name: 'Bank',
          effect: 'hold',
          removal: undefined,
          criteria: [formulaOf('from_domain', 'is', 'bank.test')],
        },
      ),
    )
    server
      .on('PUT /api/v1/rules/rul_bank', refused(409, 'rules_changed'))
      .on(list, ok(ruleListOf([home, elsewhere], { version: 5 })))
    await user.type(screen.getByLabelText('Name'), '!')
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Save rule' }).getAttribute('aria-disabled')).toBe(
        'false',
      ),
    )
    await user.click(screen.getByRole('button', { name: 'Save rule' }))
    await user.click(
      within(await screen.findByRole('dialog')).getByRole('button', { name: 'Save' }),
    )
    await screen.findByText('This rule was changed in another window.')
    expect(screen.getByText('From bank.example or bank.test')).toBeTruthy()
    await user.click(screen.getByRole('button', { name: 'Edit the saved one instead' }))
    expect(screen.queryByText('This rule was changed in another window.')).toBeNull()
    expect(screen.getByText('bank.test')).toBeTruthy()
  })

  it('says there is no such rule', async () => {
    rulesServer()
    await open('/rules/rul_gone')
    await screen.findByRole('heading', { level: 1, name: "There's no rule of yours here." })
  })

  it('previews the mail it applies to, and one message as it would leave', async () => {
    rulesServer()
      .on('POST /api/v1/rules/check', echoCheck())
      .on('POST /api/v1/rules/preview', (asked) =>
        (asked.body as { message?: string }).message
          ? ok({
              draft_id: 'rdr_x',
              subject: 'Call me at [removed]',
              text: 'I live at [removed].',
              removed: {
                parts: ['sender'],
                shapes: { phone_number: 1 },
                sentences: 1,
                of: 3,
                attachments: 2,
              },
            })
          : ok({
              draft_id: 'rdr_x',
              page: { messages: [summary(1), summary(2)], needs_model: ['m9'], unknown: [] },
            }),
      )
    const { user } = await open('/rules/rul_home')
    await screen.findByRole('heading', { level: 1, name: 'Home address' })
    await user.selectOptions(await screen.findByRole('combobox', { name: 'Mailbox' }), work.id)
    await user.click(screen.getByRole('button', { name: 'Show the mail it applies to' }))
    await screen.findByText(/2 of the newest emails\. 1 more only a model can decide/)
    expect(server.asked('POST /api/v1/rules/preview')[0].body).toMatchObject({
      mailbox: work.id,
      limit: 50,
    })
    await user.click(screen.getAllByRole('button', { name: 'See it as it would leave' })[0])
    await screen.findByText('Call me at [removed]')
    expect(screen.getByText('The sender taken out whole')).toBeTruthy()
    expect(screen.getByText('Phone numbers: 1 taken out')).toBeTruthy()
    expect(screen.getByText('1 of 3 sentences taken out')).toBeTruthy()
    expect(screen.getByText('2 attachments taken out')).toBeTruthy()
    expect(server.asked('POST /api/v1/rules/preview')[1].body).toMatchObject({
      message: summary(1).id,
    })
    await user.click(screen.getByRole('button', { name: 'Ask the model too' }))
    await waitFor(() =>
      expect(server.asked('POST /api/v1/rules/preview')[2].body).toMatchObject({
        ask_model: true,
      }),
    )
    await expectAccessible()
  })
})
