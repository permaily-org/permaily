import { expect, test } from '@playwright/test'
import { corpus, observe, requests, reset, signIn, watchDialogs } from './suite.ts'

// ADR 0026, "Tests", 26 and 27: the hostile corpus run twice, so neither line hides the other's failures.

// Run A's known failures: an engine that opens a connection for a resource hint of the raw message under the frame's
// CSP. The sanitiser drops every <link>, so the document the frame is given never has one (run B asserts zero
// connections and lookups in every engine); what fails is the frame's own line for that hint. Only that one assertion
// is isolated: every other assertion of the test holds in that engine too, and the connections opened must be exactly
// the known ones, so the test fails if the engine opens another, or stops opening this one.
const knownHint: Record<string, { reason: string; connects: string[] }> = {
  'webkit 05-remote-references': {
    reason:
      "WebKit (Playwright 1.65) opens a CONNECT to the host of <link rel=preconnect> under the frame's CSP default-src 'none', and sends nothing through it (seen 2026-11-12)",
    connects: ['pre.evil.example:443'],
  },
}

test.describe('26: run A, the frame alone', () => {
  for (const name of corpus) {
    test(name, async ({ page, browserName }, info) => {
      const known = knownHint[`${browserName} ${name}`]
      if (known) info.annotations.push({ type: 'known', description: known.reason })
      const dialogs = watchDialogs(page)
      await signIn(page)
      await reset(page)
      await page.goto(`/test/viewer-raw/${name}`)
      const state = () =>
        page.evaluate(() => ({
          html: document.documentElement.outerHTML,
          cookie: document.cookie,
          storage: localStorage.length + sessionStorage.length,
          url: location.href,
          name: window.name,
        }))
      const before = await state()
      await page.waitForTimeout(1500)
      // The frame did not navigate (a meta refresh included), and nothing in it reached the app's page.
      const frame = page.frames().find((f) => f !== page.mainFrame())
      expect(frame?.url()).toBe(`https://app.permaily.test/test/raw/${name}`)
      expect(await state()).toEqual(before)
      expect(before.cookie).toBe('marker=app')
      expect(dialogs).toEqual([])
      // No script ran: the marker a script of the corpus would set (17-execution-marker) is unset in the frame's
      // document, read through the automation API, which needs no script of the page's.
      expect(await page.frameLocator('iframe').locator('html').getAttribute('data-ran')).toBeNull()
      const seen = await requests(page)
      // No request reached the internet: no image, font, stylesheet, media or frame, and no script ran to send one.
      expect(seen.internet).toEqual([])
      // The app's host saw the viewer and the frame's document, and nothing the message named.
      expect(seen.app.map((r) => r.path).filter((p) => p !== '/favicon.ico')).toEqual([
        `/test/viewer-raw/${name}`,
        `/test/raw/${name}`,
      ])
      // No connection was opened and no name looked up, prefetch hints included. Where an engine does it anyway for a
      // hint under the frame's CSP, that is a finding against the frame's line for that hint (ADR 0026, "What holds
      // each guarantee"), kept as a known failure of that engine with its reason (knownHint above).
      if (seen.connects.length > 0)
        observe(info, `run A ${name}: connections opened`, seen.connects)
      if (seen.lookups.length > 0)
        observe(info, `run A ${name}: names looked up`, [...new Set(seen.lookups)])
      expect(seen.connects).toEqual(known?.connects ?? [])
      expect(seen.lookups).toEqual([])
    })
  }
})

// The DOM each browser builds from the sanitiser's output: elements, namespaces, attributes, links' style, and
// what covers what.
function inspect() {
  const allowed = new Set(
    'html head body meta style a abbr address article aside b bdi bdo big blockquote br caption center cite code col colgroup dd del dfn div dl dt em figcaption figure font footer h1 h2 h3 h4 h5 h6 header hr i img ins kbd li main mark nav ol p pre q rp rt ruby s samp section small span strike strong sub sup table tbody td tfoot th thead time tr tt u ul var wbr'.split(
      ' ',
    ),
  )
  const common = new Set(['dir', 'lang', 'title', 'align', 'valign', 'class', 'id', 'style'])
  const cell =
    'width height border cellpadding cellspacing bgcolor colspan rowspan nowrap scope headers'
  const byElement: Record<string, string> = {
    table: cell,
    tr: cell,
    td: cell,
    th: cell,
    col: 'span width',
    colgroup: 'span width',
    ol: 'start type reversed',
    li: 'value',
    font: 'color face size',
    time: 'datetime',
    img: 'src alt width height border hspace vspace',
    a: 'href target rel',
    html: 'lang dir translate',
    body: 'bgcolor text',
    meta: 'charset name content',
  }
  const problems: string[] = []
  for (const el of document.querySelectorAll('*')) {
    const tag = el.localName
    if (el.namespaceURI !== 'http://www.w3.org/1999/xhtml')
      problems.push(`${tag} in ${el.namespaceURI}`)
    if (!allowed.has(tag)) problems.push(`<${tag}>`)
    for (const a of el.attributes) {
      const ok =
        (common.has(a.name) && !['meta', 'head', 'style'].includes(tag)) ||
        (byElement[tag] ?? '').split(' ').includes(a.name)
      if (!ok) problems.push(`<${tag} ${a.name}>`)
      if (
        a.name === 'href' &&
        !/^\/inbox\/[^/]+\/[^/]+\/links\/(0|[1-9][0-9]{0,2})\.[A-Za-z0-9_-]{16}$/.test(a.value)
      )
        problems.push(`href ${a.value}`)
      if (a.name === 'src' && !/^data:image\/(png|jpeg|gif|webp);base64,/.test(a.value))
        problems.push(`src ${a.value.slice(0, 40)}`)
    }
  }
  const width = document.documentElement.clientWidth
  for (const a of document.querySelectorAll('a')) {
    const s = getComputedStyle(a)
    if (
      s.color !== 'rgb(26, 79, 214)' ||
      s.backgroundColor !== 'rgb(255, 255, 255)' ||
      !s.textDecorationLine.includes('underline')
    )
      problems.push(`a link's style: ${s.color} on ${s.backgroundColor}, ${s.textDecorationLine}`)
    // A float the sender wrote stays (ADR 0026, "CSS"): a floated box's display is blockified to block.
    const display = s.cssFloat === 'none' ? 'inline-block' : 'block'
    // Every link is positioned with the largest z-index: painted above everything the message draws, and the target
    // of the click at every point of its box (ADR 0026, "What each link carries").
    if (
      s.display !== display ||
      s.visibility !== 'visible' ||
      s.opacity !== '1' ||
      s.position !== 'relative' ||
      s.zIndex !== '2147483647'
    )
      problems.push(
        `a link's box: ${s.display} ${s.visibility} ${s.opacity} ${s.position} ${s.zIndex}`,
      )
    a.scrollIntoView({ block: 'center', inline: 'center' })
    // A link that holds something to show and has no box at all (an ancestor hidden it) fails.
    if (a.getClientRects().length === 0 && (a.textContent?.trim() || a.querySelector('img')))
      problems.push(`a link with no box: ${a.textContent?.trim().slice(0, 40)}`)
    for (const r of a.getClientRects()) {
      // A link with something to show must show it at a size one can see and press.
      if (r.width < 4 || r.height < 4) {
        if (a.textContent?.trim() || a.querySelector('img'))
          problems.push(`a link too small: ${r.width}x${r.height}`)
        continue
      }
      if (r.left < -1 || r.right > width + 1)
        problems.push(`a link outside the document's width: ${r.left}..${r.right} of ${width}`)
      // The corners, 2 px in: Chromium snaps the clip of an overflow:hidden box of fractional width, so the
      // point 1 px inside its bottom-right corner hit-tests to the box beneath it (seen on 2026-11-12;
      // Firefox and WebKit hit the link there; every engine hits it 1.5 px in).
      const points = [
        [r.left + r.width / 2, r.top + r.height / 2],
        [r.left + 2, r.top + 2],
        [r.right - 2, r.top + 2],
        [r.left + 2, r.bottom - 2],
        [r.right - 2, r.bottom - 2],
      ]
      for (const [x, y] of points) {
        if (x < 0 || y < 0 || x >= window.innerWidth || y >= window.innerHeight) continue
        const hit = document.elementFromPoint(x, y)
        if (hit && hit !== a && !a.contains(hit))
          problems.push(`a link covered at ${x},${y} by <${hit.localName}>`)
      }
    }
  }
  // No link lies over content that is not its own: the text of everything outside the links, hit-tested at the
  // centre of each of its line boxes, is not under a link (a link escaped from a box of height 0 would be).
  const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT)
  for (let t = walker.nextNode(); t; t = walker.nextNode()) {
    const parent = t.parentElement
    if (!parent || parent.closest('a') || !t.textContent?.trim()) continue
    parent.scrollIntoView({ block: 'center', inline: 'center' })
    const range = document.createRange()
    range.selectNodeContents(t)
    for (const r of range.getClientRects()) {
      if (r.width < 4 || r.height < 4) continue
      const [x, y] = [r.left + r.width / 2, r.top + r.height / 2]
      if (x < 0 || y < 0 || x >= window.innerWidth || y >= window.innerHeight) continue
      const hit = document.elementFromPoint(x, y)
      if (hit?.closest('a'))
        problems.push(`a link over "${t.textContent.trim().slice(0, 30)}" at ${x},${y}`)
    }
  }
  // Nothing but the links positioned, layered or hidden in any computed style. The properties that make a box
  // positioned or give it a stacking context (CSS 2.1 9.9 and Appendix E; CSS Positioned Layout 3; MDN, "Stacking
  // context", looked up on 2026-11-13): position, z-index, opacity, transform and its individual properties,
  // perspective, filter, backdrop-filter, clip-path, mask, mix-blend-mode, isolation, will-change, contain,
  // container-type, view-transition-name, offset-path; and a flex or grid container's children with a z-index.
  const stacking: Array<[string, (v: string) => boolean]> = [
    ['position', (v) => v === 'static'],
    ['z-index', (v) => v === 'auto'],
    ['opacity', (v) => v === '1'],
    ['transform', (v) => v === 'none'],
    ['translate', (v) => v === 'none' || v === ''],
    ['rotate', (v) => v === 'none' || v === ''],
    ['scale', (v) => v === 'none' || v === ''],
    ['perspective', (v) => v === 'none'],
    ['filter', (v) => v === 'none'],
    ['backdrop-filter', (v) => v === 'none' || v === ''],
    ['clip-path', (v) => v === 'none'],
    ['mask-image', (v) => v === 'none' || v === ''],
    ['mix-blend-mode', (v) => v === 'normal'],
    ['isolation', (v) => v === 'auto'],
    ['will-change', (v) => v === 'auto'],
    ['contain', (v) => v === 'none'],
    ['container-type', (v) => v === 'normal' || v === ''],
    ['view-transition-name', (v) => v === 'none' || v === ''],
    ['offset-path', (v) => v === 'none' || v === ''],
    ['display', (v) => !/flex|grid/.test(v)],
  ]
  for (const el of document.body.querySelectorAll('*')) {
    if (el.localName === 'a') continue
    const s = getComputedStyle(el)
    for (const [prop, ok] of stacking) {
      const v = s.getPropertyValue(prop)
      if (!ok(v)) problems.push(`<${el.localName}> ${prop}: ${v}`)
    }
  }
  return problems
}

test.describe('27: run B, the sanitiser alone', () => {
  for (const name of corpus) {
    test(name, async ({ page }) => {
      const dialogs = watchDialogs(page)
      await reset(page)
      await page.goto(`/test/plain/${name}`)
      await page.waitForTimeout(1500)
      expect(page.url()).toBe(`https://app.permaily.test/test/plain/${name}`)
      expect(dialogs).toEqual([])
      expect(await page.evaluate(inspect)).toEqual([])
      // No script ran: the marker 17-execution-marker's vectors would set.
      expect(await page.locator('html').getAttribute('data-ran')).toBeNull()
      const seen = await requests(page)
      // Nothing at all reached anywhere else, no connection or lookup included.
      expect(seen.internet).toEqual([])
      expect(seen.connects).toEqual([])
      expect(seen.lookups).toEqual([])
      expect(seen.app.map((r) => r.path).filter((p) => p !== '/favicon.ico')).toEqual([
        `/test/plain/${name}`,
      ])
    })
  }
})
