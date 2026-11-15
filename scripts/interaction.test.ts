// The cursor rule of packages/ui/interaction.css (docs/design.md, "Design language"): the hand on everything a
// person can click, not-allowed on what is disabled, and both apps load it.

import { describe, expect, test } from 'bun:test'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

const root = join(import.meta.dir, '..')
const read = (path: string) => readFileSync(join(root, path), 'utf8')
const css = read('packages/ui/interaction.css').replace(/\/\*[\s\S]*?\*\//g, '')

/** The selectors inside the :where() of the rule that sets this cursor. */
function selectorsFor(cursor: string): string[] {
  const rule = new RegExp(`:where\\(([^{}]*)\\)\\s*\\{\\s*cursor:\\s*${cursor};\\s*\\}`)
  const match = css.match(rule)
  if (!match) throw new Error(`no :where() rule sets cursor: ${cursor}`)
  // Split on the commas of the top level only: a selector such as input:is(...) has commas of its own.
  const parts: string[] = []
  let depth = 0
  let current = ''
  for (const ch of match[1]) {
    if (ch === '(') depth++
    if (ch === ')') depth--
    if (ch === ',' && depth === 0) {
      parts.push(current.trim())
      current = ''
    } else current += ch
  }
  parts.push(current.trim())
  return parts.map((s) => s.replace(/\s+/g, ' '))
}

describe('the cursor rule', () => {
  const pointer = selectorsFor('pointer')
  const disabled = selectorsFor('not-allowed')

  test('gives the hand to buttons, links, summaries, selects and the clickable roles', () => {
    for (const s of ['a[href]', 'button', 'summary', 'select']) expect(pointer).toContain(s)
    for (const role of ['button', 'link', 'tab', 'menuitem', 'menuitemradio', 'option', 'switch']) {
      expect(pointer).toContain(`[role='${role}']`)
    }
    const inputs = pointer.find((s) => s.startsWith('input:is('))
    for (const type of ['checkbox', 'radio', 'file', 'submit', 'button']) {
      expect(inputs).toContain(`[type='${type}']`)
    }
  })

  test('leaves text fields with the text cursor', () => {
    const named = pointer.join(' ')
    expect(named).not.toContain('textarea')
    for (const type of ['text', 'email', 'search', 'password', 'number']) {
      expect(named).not.toContain(`[type='${type}']`)
    }
    expect(pointer).not.toContain('input')
    expect(pointer).not.toContain('label')
  })

  test('gives disabled controls not-allowed, after the hand so it wins', () => {
    for (const s of [':disabled', '[disabled]', "[aria-disabled='true']"])
      expect(disabled).toContain(s)
    expect(css.indexOf('cursor: not-allowed')).toBeGreaterThan(css.indexOf('cursor: pointer'))
  })

  test('is loaded by the web app beneath its utilities and by the site', () => {
    expect(read('web/src/styles/index.css')).toContain(
      "@import '../../../packages/ui/interaction.css' layer(base);",
    )
    expect(read('site/src/styles/global.css')).toContain(
      "@import '../../../packages/ui/interaction.css';",
    )
  })

  test("is not outranked by the site's own rule for buttons", () => {
    const button = read('site/src/styles/global.css').match(/\nbutton \{([^}]*)\}/)
    expect(button?.[1]).not.toContain('cursor')
  })
})
