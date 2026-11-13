import { expect, test } from '@playwright/test'
import { cases, observe, requests, reset, signIn, urlTable, watchDialogs } from './suite.ts'

// ADR 0026, "Tests", 28 to 31, and what the release gate asks to see in each browser (docs/acceptance.md).

const frameHeaders = {
  'content-type': 'text/html; charset=utf-8',
  'content-security-policy':
    "sandbox allow-popups allow-popups-to-escape-sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data:; form-action 'none'; base-uri 'none'; frame-ancestors https://app.permaily.test",
  'x-content-type-options': 'nosniff',
  'cross-origin-resource-policy': 'same-origin',
  'referrer-policy': 'no-referrer',
  'cache-control': 'no-store',
  'x-dns-prefetch-control': 'off',
}

test('28: the frame on the real route: its session, its headers, a link opens one tab without an opener', async ({
  page,
  context,
}, info) => {
  const dialogs = watchDialogs(page)
  await signIn(page)
  const { box, cases: list } = await cases(page)
  const links = list.find((c) => c.Name === 'links')?.Message as string
  const route = `/api/v1/mailboxes/${box}/messages/${links}/html`
  await reset(page)
  const answered = page.waitForResponse((r) => r.url().endsWith(route))
  await page.goto('/test/viewer/links')
  const frameResponse = await answered
  const seen = await requests(page)
  const frameRequest = seen.app.find((r) => r.path === route)
  // The release gate's first row: the frame's first navigation from the app carries the Strict session.
  observe(info, 'the frame request', frameRequest)
  expect(frameRequest?.session).toBe(true)
  expect(frameResponse.status()).toBe(200)
  const headers = frameResponse.headers()
  for (const [k, v] of Object.entries(frameHeaders)) expect(headers[k], k).toBe(v)

  const frame = page.frameLocator('iframe')
  await expect(frame.getByRole('link', { name: 'https://bank.example/pay' })).toBeVisible()
  await reset(page)
  const [tab] = await Promise.all([
    context.waitForEvent('page'),
    frame.getByRole('link', { name: 'https://bank.example/pay' }).click(),
  ])
  await tab.waitForLoadState()
  expect(tab.url()).toMatch(new RegExp(`/inbox/${box}/${links}/links/0\\.[A-Za-z0-9_-]{16}$`))
  expect(await tab.evaluate(() => window.opener)).toBeNull()
  expect(context.pages()).toHaveLength(2)
  // The frame's URL is the one it was given, and the page's.
  expect(page.frames()[1].url()).toBe(`https://app.permaily.test${route}`)
  expect(page.url()).toBe('https://app.permaily.test/test/viewer/links')
  // The link page's tab: its first request (the page) and its call to the API.
  await expect(tab.getByRole('heading', { level: 1, name: /This link leads to/ })).toBeVisible()
  const tabRequests = (await requests(page)).app
  const document = tabRequests.find(
    (r) => r.path.includes('/links/') && !r.path.startsWith('/api/'),
  )
  const call = tabRequests.find((r) => r.path.startsWith('/api/') && r.path.includes('/links/'))
  observe(info, "the link page's first request", document)
  observe(info, "the link page's call to the API", call)
  expect(call?.session).toBe(true)
  expect(call?.status).toBe(200)
  await expect(tab.getByRole('button', { name: 'Open evil.example' })).toBeVisible()
  await expect(
    tab.getByText('The message showed bank.example, but this link leads to evil.example.'),
  ).toBeVisible()
  expect((await requests(page)).internet).toEqual([])
  expect(dialogs).toEqual([])
})

test('28: the frame route opened in a tab answers 404, without the message', async ({
  page,
}, info) => {
  await signIn(page)
  const { box, cases: list } = await cases(page)
  const route = `/api/v1/mailboxes/${box}/messages/${list[0].Message}/html`
  await reset(page)
  const res = await page.goto(route)
  const seen = (await requests(page)).app.find((r) => r.path === route)
  observe(info, 'the frame route opened as a page', {
    status: res?.status(),
    dest: seen?.dest,
    site: seen?.site,
  })
  expect(res?.status()).toBe(404)
  expect(await page.textContent('body')).toContain("This message can't be shown here.")
  // Back to it from history: the same answer.
  await page.goto('/test/blank')
  const back = await page.goBack()
  observe(info, 'the frame route from history', back?.status() ?? 'restored from cache')
  expect(await page.textContent('body')).not.toContain('Pay at')
})

test("a sandboxed document's own requests to the API are refused and get nothing of the account", async ({
  page,
  context,
  browserName,
}, info) => {
  // ADR 0026, "Corrected while building pull request 5": every request under /api/ that says another site made it
  // is refused before a session is looked up (httpapi.RefuseCrossSite), so this holds in every engine, whether or
  // not it attaches the SameSite=Strict session to a sandboxed (opaque origin) document's requests. Which engines
  // attach it is recorded below and asserts nothing: Playwright's WebKit 27.2 does, Chromium and Firefox do not.
  await signIn(page)
  await reset(page)
  await page.goto('/test/viewer-sandboxed')
  const frame = page.frameLocator('iframe')
  await expect(frame.locator('#tab')).toBeVisible()
  await page.waitForTimeout(1000)
  const [tab] = await Promise.all([context.waitForEvent('page'), frame.locator('#tab').click()])
  await tab.waitForLoadState()
  await frame.locator('#go').click()
  await page.waitForTimeout(1000)
  const seen = (await requests(page)).app.filter((r) => r.path.includes('from='))
  const api = seen.filter((r) => r.path.startsWith('/api/'))
  // An image, the frame's own navigation and a new tab: each to the API, each refused.
  expect(api.map((r) => r.path).sort()).toEqual(
    ['/api/v1/me?from=click', '/api/v1/me?from=img', '/api/v1/me?from=tab'].sort(),
  )
  for (const r of api) {
    expect(r.site, r.path).not.toBe('same-origin')
    expect(r.status, r.path).toBe(403)
  }
  // What the tab shows: the fixed refusal, nothing of the account (read as text: an engine may show JSON in a viewer
  // of its own). The frame's navigation got the same 403 (above), which the app's frame-ancestors 'none' keeps any
  // engine from showing in a frame at all.
  await expect(tab.locator('body')).toContainText('this request did not come from the app')
  const inTab = (await tab.textContent('body')) ?? ''
  expect(inTab).not.toContain('alice')
  expect(inTab).not.toContain('acc_')
  // Informational, asserts nothing: which requests of the sandboxed document carried the session cookie.
  const carried = seen.filter((r) => r.session).map((r) => r.path)
  observe(info, "the sandboxed document's requests", seen)
  observe(
    info,
    `${browserName} attaches the session to a sandboxed document's requests`,
    carried.length > 0 ? carried : 'none',
  )
})

test('a download started from the app is same-origin and answered', async ({ page }, info) => {
  // ADR 0026, "Corrected while building pull request 5": the rule that refuses another site leaves the app's own
  // downloads alone. The message view's "Download original" is a link with `download`: a navigation the app's page
  // starts: Sec-Fetch-Site: same-origin in Chromium 158 and Firefox 159; Playwright's WebKit 27.2 sends no Fetch
  // Metadata on it at all (seen on 2026-11-12), which the rule leaves as it was.
  await signIn(page)
  const { box, cases: list } = await cases(page)
  const links = list.find((c) => c.Name === 'links')?.Message as string
  await page.goto(`/inbox/${box}/${links}`)
  const original = page.getByRole('link', { name: 'Download original' })
  await expect(original).toBeVisible()
  await reset(page)
  const [download] = await Promise.all([page.waitForEvent('download'), original.click()])
  expect(download.suggestedFilename()).toBe(`${links}.eml`)
  const seen = (await requests(page)).app.find((r) => r.path.endsWith('/raw'))
  observe(info, 'the download of the original', seen)
  expect(['same-origin', '', undefined]).toContain(seen?.site)
  expect(seen?.status).toBe(200)
})

test('29: the link page contacts nothing until Open, then one request without a referrer, in a tab without an opener', async ({
  page,
  context,
}) => {
  await signIn(page)
  const { box, cases: list } = await cases(page)
  const links = list.find((c) => c.Name === 'links')?.Message as string
  // The token of the first link, from the frame's own document.
  const doc = await (
    await page.request.get(`/api/v1/mailboxes/${box}/messages/${links}/html`, {
      headers: { 'Sec-Fetch-Dest': 'iframe', 'Sec-Fetch-Site': 'same-origin' },
    })
  ).text()
  const path = /href="(\/inbox\/[^"]+\/links\/0\.[^"]+)"/.exec(doc)?.[1] as string
  expect(path).toBeTruthy()
  await reset(page)
  await page.goto(path)
  const open = page.getByRole('button', { name: 'Open evil.example' })
  await expect(open).toBeVisible()
  await expect(page.getByText('https://evil.example/pay?id=7')).toBeVisible()
  // Hover, focus and a wait: nothing reaches the destination.
  await open.hover()
  await open.focus()
  await page.waitForTimeout(1500)
  expect(
    await page.evaluate(() =>
      [...document.querySelectorAll('*')].flatMap((el) =>
        ['href', 'src', 'srcset', 'action', 'formaction', 'ping', 'poster', 'data']
          .map((a) => el.getAttribute(a) ?? '')
          .filter((v) => v.includes('evil.example')),
      ),
    ),
  ).toEqual([])
  let seen = await requests(page)
  expect(seen.internet).toEqual([])
  expect(seen.connects).toEqual([])
  const [tab] = await Promise.all([context.waitForEvent('page'), open.click()])
  await tab.waitForLoadState()
  await expect(page.getByText('Opened in a new tab. You can close this one.')).toBeVisible()
  seen = await requests(page)
  // One request from Permaily's side: the destination itself. What the destination's own page asks for once it
  // has loaded (Firefox asks for its /favicon.ico, with the destination as referrer) is the destination's.
  const ours = seen.internet.filter((r) => !(r.referer ?? '').startsWith('https://evil.example/'))
  expect(ours).toHaveLength(1)
  expect(ours[0]).toMatchObject({ host: 'evil.example', path: '/pay?id=7' })
  // No referrer: the server leaves the field out when the request had none.
  expect(ours[0].referer ?? '').toBe('')
  expect(await tab.evaluate(() => window.opener)).toBeNull()

  // A token that no longer names the link: link_changed, and no button.
  const changed = path.replace(/\.[A-Za-z0-9_-]{16}$/, '.AAAAAAAAAAAAAAAA')
  await page.goto(changed)
  await expect(
    page.getByText(
      'This link changed since the message was opened. Close this tab, open the message again, then the link.',
    ),
  ).toBeVisible()
  await expect(page.getByRole('button', { name: /^Open/ })).toHaveCount(0)
})

test('30: Go and this engine agree on every link', async ({ page }) => {
  const results = await page.evaluate(
    (rows) =>
      rows.map((row) => {
        if (row.kept) {
          const u = new URL(row.url as string)
          return {
            input: row.input,
            got: [u.href, u.protocol, u.hostname, u.port, u.host],
            want: [row.url, row.protocol, row.hostname, row.port ?? '', row.host],
          }
        }
        if (row.ipv4) {
          return {
            input: row.input,
            got: /^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$/.test(new URL(row.input).hostname),
            want: true,
          }
        }
        return null
      }),
    urlTable.urls,
  )
  for (const r of results) if (r) expect(r.got, r.input).toEqual(r.want)
})

test('31: after signing out, Back does not show the message until me and the message answer again', async ({
  page,
}, info) => {
  await signIn(page)
  const { box, cases: list } = await cases(page)
  const links = list.find((c) => c.Name === 'links')?.Message as string
  await page.addInitScript(() => {
    addEventListener('pageshow', (e) => {
      if (e.persisted) (window as unknown as { restored: number }).restored = 1
    })
  })
  await page.goto(`/inbox/${box}/${links}`)
  await expect(page.getByRole('heading', { level: 1, name: 'links' })).toBeVisible()
  await expect(page.locator('iframe')).toHaveCount(1)
  await page.request.get('/test/sign-out')
  await page.goto('/test/blank')
  await reset(page)
  await page.goBack()
  await expect(page).toHaveURL(/\/sign-in/, { timeout: 15_000 })
  await expect(page.locator('iframe')).toHaveCount(0)
  const restored = await page.evaluate(
    () => (window as unknown as { restored?: number }).restored === 1,
  )
  const seen = (await requests(page)).app.filter((r) => r.path.startsWith('/api/'))
  observe(info, 'restored from the back/forward cache', restored)
  observe(
    info,
    'asked after Back',
    seen.map((r) => `${r.path} ${r.status}`),
  )
  // Whatever the browser did, nothing of the message was answered after signing out.
  for (const r of seen)
    expect(r.status === 200 && r.path.includes('/messages/'), r.path).toBe(false)
})

test('release gate: without Fetch Metadata the route serves the document, and it stays sandboxed', async ({
  page,
}, info) => {
  const dialogs = watchDialogs(page)
  await signIn(page)
  const { cases: list } = await cases(page)
  const hostile = list.find((c) => c.Name === '02-event-attributes')?.Message as string
  await reset(page)
  const res = await page.goto(`/test/nometa/${hostile}`)
  expect(res?.status()).toBe(200)
  expect(res?.headers()['content-security-policy']).toBe(frameHeaders['content-security-policy'])
  await expect(page.getByText('kept div')).toBeVisible()
  await page.waitForTimeout(1500)
  // A top-level document with the CSP sandbox: an opaque origin, so no storage and no cookie, and nothing ran.
  const sandboxed = await page.evaluate(() => {
    let storage = 'readable'
    try {
      void localStorage.length
    } catch {
      storage = 'throws'
    }
    return {
      origin: self.origin,
      storage,
      cookie: (() => {
        try {
          return document.cookie
        } catch {
          return 'throws'
        }
      })(),
    }
  })
  observe(info, 'the frame route without Fetch Metadata, opened as a page', sandboxed)
  expect(sandboxed.origin).toBe('null')
  expect(sandboxed.storage).toBe('throws')
  expect(dialogs).toEqual([])
  const seen = await requests(page)
  expect(seen.internet).toEqual([])
  expect(seen.connects).toEqual([])
  expect(seen.app.map((r) => r.path).filter((p) => p !== '/favicon.ico')).toEqual([
    `/test/nometa/${hostile}`,
  ])
})
