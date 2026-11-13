import { describe, expect, test } from 'bun:test'
import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { render } from '@react-email/render'
import { kinds } from '../src/blocks.tsx'
import { type Message, MessageView } from '../src/message.tsx'
import { actions, templates } from '../src/templates.tsx'
import { colorsCss, parseTokens, tokenNames, tokens } from '../src/tokens.ts'

const root = join(import.meta.dir, '..')
const out = join(root, '../../internal/domain/mail/templates')
const fixtures = readdirSync(join(root, 'fixtures'))
  .filter((f) => f.endsWith('.json'))
  .map((f) => JSON.parse(readFileSync(join(root, 'fixtures', f), 'utf8')) as Message)

describe('tokens', () => {
  test('are the values of packages/ui/colors.css', () => {
    const all = parseTokens(readFileSync(colorsCss, 'utf8'))
    for (const name of tokenNames)
      expect(tokens[name]).toEqual(all.get(name) ?? { light: '', dark: '' })
    expect(all.get('highlight')).toEqual({ light: '#ddf45b', dark: '#ddf45b' })
  })

  test('the mail uses neither black bars nor the highlight', () => {
    expect(tokenNames as readonly string[]).not.toContain('bar')
    expect(tokenNames as readonly string[]).not.toContain('highlight')
  })
})

describe('templates', () => {
  test('are the same every time they are built', async () => {
    expect(await templates()).toEqual(await templates())
  })

  test('are the committed ones', async () => {
    for (const [name, html] of await templates()) {
      expect(readFileSync(join(out, name), 'utf8')).toBe(html)
    }
    expect(readdirSync(out).sort()).toEqual([...(await templates()).keys()].sort())
  })

  test('have one for each kind of block, and the layout dispatches to each', async () => {
    const files = await templates()
    for (const kind of kinds) {
      expect(files.get(`${kind}.html`)).toContain(`{{define "${kind}"}}`)
      expect(files.get('layout.html')).toContain(`{{template "${kind}" .}}`)
    }
  })

  test('refuse a marker left over', () => {
    expect(() => actions('<p>@@.Text</p>')).toThrow()
    expect(() => actions('<pm-other></pm-other>')).toThrow()
    expect(actions('<a href="@@.URL@@">@@.URL@@</a>')).toBe('<a href="{{.URL}}">{{.URL}}</a>')
  })
})

describe('preview', () => {
  test('has every message the service sends', () => {
    expect(fixtures.length).toBeGreaterThanOrEqual(30)
    for (const f of fixtures) for (const b of f.blocks) expect(kinds).toContain(b.kind)
  })

  test('renders each message with its words and its one link', async () => {
    for (const f of fixtures) {
      const html = await render(<MessageView message={f} />)
      for (const b of f.blocks) {
        if (b.kind === 'code') expect(html).toContain(b.text)
        if (b.kind === 'action') expect(html).toContain(`href="${b.url.replaceAll('&', '&amp;')}"`)
      }
      expect(html).not.toMatch(/<img|<script|<link|url\(/)
    }
  })
})
