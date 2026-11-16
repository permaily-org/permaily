import { expect, type Page, test } from '@playwright/test'
import { PlayedApi, Vendor } from './played.ts'

/*
  V63 (ADR 0030), against the app as the enclave serves it: the built app through the real router (its security
  headers), the app's host (its one decision about a path) and the SPA's handler (internal/webui), composed by
  web/tests/e2e/server. The API is played in the browser's context, as in consent.spec.ts. What only the production
  server decides is here: the headers, framing, every spelling of the consent page it serves, and opening a page of the
  app directly or by a reload.
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

const secrets = [
  'st8te-secret',
  'Q0hBTExFTkdFLWNoYWxsZW5nZS1jaGFsbGVuZ2',
  'claude-code-client-metadata',
]

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
  for (const secret of [...secrets, 'pmq_played']) expect(kept).not.toContain(secret)
}

const screenHeading = /asks to read your mail\.$/

test('the consent page has the production headers, and sends no Referer and logs nothing through Deny', async ({
  page,
  context,
}) => {
  api.signedIn = true
  const refers: string[] = []
  const logs: string[] = []
  page.on('request', (r) => {
    // Every Referer but the vendor's own page's, which the browser sends on the way in to the app.
    const referer = r.headers().referer
    if (referer && referer !== `${vendor.origin}/`) refers.push(referer)
  })
  page.on('console', (m) => logs.push(m.text()))
  const response = page.waitForResponse((r) => new URL(r.url()).pathname === '/oauth/authorize')
  await vendor.connect(page, app)
  const headers = (await response).headers()
  expect(headers['referrer-policy']).toBe('no-referrer')
  expect(headers['content-security-policy']).toContain("frame-ancestors 'none'")
  await expect(page.getByRole('heading', { name: screenHeading })).toBeVisible()
  await expectNothingKept(page)
  // What is enforced of the history: the entry the app shows is the cleaned address. The browser may keep the address
  // it was first sent to elsewhere in its own records of the navigation (docs/threat-model.md).
  const cdp = await context.newCDPSession(page)
  const history = await cdp.send('Page.getNavigationHistory')
  expect(history.entries[history.currentIndex].url).toBe(`${app}/oauth/authorize`)
  await page.getByRole('button', { name: /^Deny/ }).click()
  await expect(page.getByRole('heading', { name: 'The vendor got an answer' })).toBeVisible()
  // The only Referer is the vendor's own, on the way in: none of the app's requests carries one.
  expect(refers).toEqual([])
  expect(logs).toEqual([])
})

test('the consent page will not open inside a vendor’s frame', async ({ page }) => {
  api.signedIn = true
  await page.goto(vendor.origin)
  const refused = page.waitForEvent('console', (m) => m.text().includes('frame-ancestors'))
  await page.evaluate((src) => {
    const frame = document.createElement('iframe')
    frame.src = src
    document.body.append(frame)
  }, `${app}/oauth/authorize?${vendor.query()}`)
  await refused
  expect(api.asked).toEqual([])
})

for (const path of ['/oauth/authorize/', '/OAuth/Authorize']) {
  test(`${path}, which the server serves as the consent page, is cleaned before signing in`, async ({
    page,
  }) => {
    const response = page.waitForResponse((r) => new URL(r.url()).pathname === path)
    await vendor.connect(page, app, path)
    expect((await response).status()).toBe(200)
    await expect(page.getByLabel('Email address')).toBeVisible()
    await expectNothingKept(page)
  })

  test(`${path} signed in shows the request, with the address cleaned`, async ({ page }) => {
    api.signedIn = true
    await vendor.connect(page, app, path)
    await expect(page.getByRole('heading', { name: screenHeading })).toBeVisible()
    expect(new URL(page.url()).pathname).toBe('/oauth/authorize')
    expect(new URL(page.url()).search).toBe('')
    await expectNothingKept(page)
  })
}

test('a vendor’s overview opens directly and on a reload', async ({ page }) => {
  api.signedIn = true
  api.connections = [PlayedApi.connection()]
  const response = await page.goto(`${app}/connections/claude.ai/overview`)
  expect(response?.status()).toBe(200)
  await expect(page.getByRole('heading', { level: 1, name: '1 connection.' })).toBeVisible()
  const again = await page.reload()
  expect(again?.status()).toBe(200)
  await expect(page.getByRole('heading', { level: 1, name: '1 connection.' })).toBeVisible()
  // The list leads there.
  await page.goto(`${app}/connections`)
  await page.getByRole('link', { name: 'claude.ai' }).click()
  await expect(page).toHaveURL(`${app}/connections/claude.ai/overview`)
})

test('Back within the app, after leaving the screen by its own link, finds nothing', async ({
  page,
}) => {
  api.signedIn = true
  api.decisionRefusal = 'too_many_connections'
  await vendor.connect(page, app)
  await page.getByRole('button', { name: /^Allow/ }).click()
  await expect(page.getByText(/Going there forgets this request/)).toBeVisible()
  await page.getByRole('link', { name: 'Your connections' }).click()
  await expect(page.getByRole('heading', { level: 1, name: 'No connections.' })).toBeVisible()
  await page.goBack()
  await expect(
    page.getByRole('heading', { name: "This request isn't here any more" }),
  ).toBeVisible()
  expect(api.asked.filter((a) => a.route === 'POST /api/v1/oauth/requests')).toHaveLength(1)
})
