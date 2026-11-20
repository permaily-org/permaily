import { expect, test } from '@playwright/test'
import { PlayedApi, Vendor } from './played.ts'

/*
  U32 (ADR 0040, pull request 7): making a rule and seeing it on the consent screen, in a real browser. The person
  builds a rule from conditions, reads the sentence the server wrote, saves it with the question that says an answer
  already being prepared can finish, and a fresh proof; then a vendor sends the browser to the consent screen, which
  lists the rule with what it does and who for. The API is played (`played.ts`) until ADR 0040's routes are composed
  into a dev enclave.
*/

const vendor = new Vendor()
let api: PlayedApi
let app = ''

test.beforeAll(async () => {
  await vendor.start()
})

test.afterAll(async () => {
  await vendor.stop()
})

test.beforeEach(async ({ context, baseURL }) => {
  app = baseURL ?? ''
  api = new PlayedApi(vendor)
  await api.install(context, app)
})

test.afterEach(() => {
  expect(api.unexpected).toEqual([])
})

test('makes a rule from conditions, saves it with a fresh proof, and sees it on the consent screen', async ({
  page,
}) => {
  api.signedIn = true
  await page.goto(`${app}/rules/new`)
  await expect(page.getByRole('heading', { name: 'What you save' })).toBeVisible()

  await page.getByLabel('Name').fill('Bank')
  await page.getByRole('button', { name: 'Add criteria' }).click()
  const criteria = page.getByRole('group', { name: 'Criteria 1, where every condition holds' })
  await criteria.getByRole('combobox', { name: 'Field' }).selectOption('from_domain')
  await criteria.getByRole('textbox', { name: 'Add to Sender domain is' }).fill('bank.example')
  await criteria.getByRole('textbox', { name: 'Add to Sender domain is' }).press('Enter')

  const saving = page.getByRole('region', { name: 'What you save' })
  await expect(saving.getByText('From bank.example')).toBeVisible()
  await expect(saving.getByText('Holds the email back')).toBeVisible()

  await page.getByRole('button', { name: 'Save rule' }).click()
  const question = page.getByRole('dialog', { name: 'Save this rule?' })
  await expect(
    question.getByText(
      'Applies to every request that begins after you save; an answer already being prepared can still finish.',
    ),
  ).toBeVisible()
  await question.getByRole('button', { name: 'Save' }).click()

  // The fresh proof, by the code of the message, then the save goes on by itself.
  const proof = page.getByRole('dialog', { name: "Confirm it's you" })
  await proof.getByRole('button', { name: 'Email me a code' }).click()
  await proof.getByLabel('Code from the message').fill('123456')
  await proof.getByRole('button', { name: 'Confirm' }).click()

  await expect(page.getByRole('heading', { level: 1, name: '1 rule of your own.' })).toBeVisible()
  expect(new URL(page.url()).pathname).toBe('/rules')
  const saved = api.asked.filter((a) => a.route === 'POST /api/v1/rules')
  expect(saved).toHaveLength(2)
  expect(saved[1]?.body).toMatchObject({
    version: 1,
    draft: {
      name: 'Bank',
      effect: 'hold',
      scope: { to: 'all' },
      criteria: [
        { v: 1, all: [{ any: [{ var: 'from_domain', op: 'is', value: 'bank.example' }] }] },
      ],
    },
  })

  await vendor.connect(page, app)
  await expect(page.getByRole('heading', { name: /asks to read your mail\.$/ })).toBeVisible()
  const rules = page.getByRole('region', { name: 'Your rules apply first' })
  await expect(rules.getByText('Bank', { exact: true })).toBeVisible()
  await expect(rules.getByText('From bank.example')).toBeVisible()
  await expect(rules.getByText('Holds the email back').first()).toBeVisible()
  await expect(rules.getByText('Sign-in codes and password resets')).toBeVisible()
})
