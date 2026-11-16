import { expect, type Page, test } from '@playwright/test'
import { PlayedApi, Vendor, withPasskey } from './played.ts'

/*
  V63 (ADR 0030): the hand-off between a vendor, the app and the API, in a real browser. The browser arrives at the
  consent screen from the vendor's site, signed out or signed in; signs in by the code of the message or by a passkey in
  the same tab; allows, with a fresh proof asked and given, or denies; and a request that is stale says so.
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

/** Nothing of the vendor's request is in the address bar, the history's state or storage. */
async function expectNothingKept(page: Page) {
  const kept = await page.evaluate(() =>
    JSON.stringify([
      window.location.href,
      window.history.state,
      { ...localStorage },
      { ...sessionStorage },
    ]),
  )
  for (const secret of [
    'st8te-secret',
    'Q0hBTExFTkdFLWNoYWxsZW5nZS1jaGFsbGVuZ2',
    'claude-code-client-metadata',
    'pmq_played',
  ]) {
    expect(kept).not.toContain(secret)
  }
}

async function signInByCode(page: Page) {
  await page.getByLabel('Email address').fill('ana@example.org')
  await page.getByRole('button', { name: 'Continue' }).click()
  await page.getByLabel('Code from the message').fill('123456')
  await page.getByRole('button', { name: 'Sign in' }).click()
}

const screenHeading = /asks to read your mail\.$/

test('signed out, signs in by code in the same tab, and allows with a fresh proof', async ({
  page,
}) => {
  expect(await vendor.connect(page, app)).toBe(`${vendor.origin}/`)
  await expect(page.getByLabel('Email address')).toBeVisible()
  await expectNothingKept(page)
  expect(api.asked.some((a) => a.route === 'POST /api/v1/oauth/requests')).toBe(false)

  await signInByCode(page)
  await expect(page.getByRole('heading', { name: screenHeading })).toBeVisible()
  expect(new URL(page.url()).pathname).toBe('/oauth/authorize')
  expect(new URL(page.url()).search).toBe('')
  const started = api.asked.find((a) => a.route === 'POST /api/v1/oauth/requests')
  expect(started?.body).toEqual({ query: vendor.query() })
  await expectNothingKept(page)

  // Allowing asks for a fresh proof, then goes on by itself, and the browser leaves for the vendor.
  await page.getByRole('button', { name: /^Allow/ }).click()
  const dialog = page.getByRole('dialog', { name: "Confirm it's you" })
  await dialog.getByRole('button', { name: 'Email me a code' }).click()
  await dialog.getByLabel('Code from the message').fill('123456')
  await dialog.getByRole('button', { name: 'Confirm' }).click()
  await expect(page.getByRole('heading', { name: 'The vendor got an answer' })).toBeVisible()
  expect(vendor.callbacks.at(-1)).toBe(
    'code=pmc_played&state=st8te-secret&iss=https%3A%2F%2Fapi.permaily.com',
  )
  const decisions = api.asked.filter((a) => a.route.endsWith('/decision'))
  expect(decisions).toHaveLength(2)
  expect(decisions[1].body).toMatchObject({ allow: true, model_notice: '2026-11-15' })

  // Back to the app: the request is not there any more.
  await page.goBack()
  await expect(
    page.getByRole('heading', { name: "This request isn't here any more" }),
  ).toBeVisible()
  await expectNothingKept(page)
})

test('signed out, signs in by passkey in the same tab, and denies', async ({ page }) => {
  await withPasskey(page)
  await vendor.connect(page, app)
  await page.getByRole('button', { name: 'Sign in with a passkey' }).click()
  await expect(page.getByRole('heading', { name: screenHeading })).toBeVisible()
  expect(api.asked.some((a) => a.route === 'POST /api/v1/auth/passkey/finish')).toBe(true)
  await page.getByRole('button', { name: /^Deny/ }).click()
  await expect(page.getByRole('heading', { name: 'The vendor got an answer' })).toBeVisible()
  expect(vendor.callbacks.at(-1)).toBe(
    'error=access_denied&state=st8te-secret&iss=https%3A%2F%2Fapi.permaily.com',
  )
})

test('signed in, arrives from the vendor straight on the screen, and a reload finds nothing', async ({
  page,
}) => {
  api.signedIn = true
  expect(await vendor.connect(page, app)).toBe(`${vendor.origin}/`)
  await expect(page.getByRole('heading', { name: screenHeading })).toBeVisible()
  await expect(page.getByText('This app runs on your computer.')).toBeVisible()
  await expectNothingKept(page)
  await page.reload()
  await expect(
    page.getByRole('heading', { name: "This request isn't here any more" }),
  ).toBeVisible()
})

test('a request that ended says so, and the browser stays', async ({ page }) => {
  api.signedIn = true
  api.decisionRefusal = 'request_ended'
  const before = vendor.callbacks.length
  await vendor.connect(page, app)
  await page.getByRole('button', { name: /^Deny/ }).click()
  await expect(page.getByRole('heading', { name: 'That request ended' })).toBeVisible()
  expect(new URL(page.url()).pathname).toBe('/oauth/authorize')
  expect(vendor.callbacks).toHaveLength(before)
})
