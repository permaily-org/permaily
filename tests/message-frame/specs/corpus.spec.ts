import { expect, test } from '@playwright/test'
import { corpus, observe, requests, reset, signIn, watchDialogs } from './suite.ts'

// ADR 0026, "Tests", 26 and 27: the hostile corpus run twice, so neither line hides the other's failures.

test.describe('26: run A, the frame alone', () => {
  for (const name of corpus) {
    test(name, async ({ page }, info) => {
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
      const seen = await requests(page)
      // No request reached the internet: no image, font, stylesheet, media or frame, and no script ran to send one.
      expect(seen.internet).toEqual([])
      // The app's host saw the viewer and the frame's document, and nothing the message named.
      expect(seen.app.map((r) => r.path).filter((p) => p !== '/favicon.ico')).toEqual([
        `/test/viewer-raw/${name}`,
        `/test/raw/${name}`,
      ])
      // DNS and connections of prefetch hints are recorded per browser, not asserted: their first line is the
      // sanitiser (ADR 0026).
      if (seen.connects.length > 0)
        observe(info, `run A ${name}: connections opened`, seen.connects)
      if (seen.lookups.length > 0)
        observe(info, `run A ${name}: names looked up`, [...new Set(seen.lookups)])
    })
  }
})

// The DOM each browser builds from the sanitiser's output: elements, namespaces, attributes, links' style, and
// what covers what.
function inspect() {
  const allowed = new Set(
    'html head body meta style a abbr address article aside b bdi bdo big blockquote br caption center cite code col colgroup dd del details dfn div dl dt em figcaption figure font footer h1 h2 h3 h4 h5 h6 header hr i img ins kbd li main mark nav ol p pre q rp rt ruby s samp section small span strike strong sub summary sup table tbody td tfoot th thead time tr tt u ul var wbr'.split(
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
    if (
      s.display !== display ||
      s.visibility !== 'visible' ||
      s.opacity !== '1' ||
      s.position !== 'static'
    )
      problems.push(`a link's box: ${s.display} ${s.visibility} ${s.opacity} ${s.position}`)
    a.scrollIntoView({ block: 'center', inline: 'center' })
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
  // Nothing positioned, layered or hidden in any computed style.
  for (const el of document.body.querySelectorAll('*')) {
    const s = getComputedStyle(el)
    if (
      s.position !== 'static' ||
      s.transform !== 'none' ||
      s.opacity !== '1' ||
      s.zIndex !== 'auto'
    )
      problems.push(`<${el.localName}> ${s.position} ${s.transform} ${s.opacity} ${s.zIndex}`)
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
