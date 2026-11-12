// The colours of the mail, read from packages/ui/colors.css when the templates are built. Mail clients have no
// CSS variables, so the values are copied into the templates; scripts/check-design-system.ts checks that every
// colour there is still one of these tokens.

import { readFileSync } from 'node:fs'
import { join } from 'node:path'

export type Pair = { light: string; dark: string }

export const tokenNames = ['paper', 'sunken', 'ink', 'ink-2', 'rule'] as const
export type TokenName = (typeof tokenNames)[number]

/** Reads every `--name: light-dark(a, b)` or `--name: a` of the stylesheet, by name. */
export function parseTokens(css: string): Map<string, Pair> {
  const out = new Map<string, Pair>()
  for (const m of css.matchAll(/--([a-z0-9-]+):\s*([^;]+);/g)) {
    const value = m[2].trim()
    const pair = /^light-dark\(\s*(#[0-9a-fA-F]{3,8})\s*,\s*(#[0-9a-fA-F]{3,8})\s*\)$/.exec(value)
    if (pair) out.set(m[1], { light: pair[1].toLowerCase(), dark: pair[2].toLowerCase() })
    else if (/^#[0-9a-fA-F]{3,8}$/.test(value)) {
      out.set(m[1], { light: value.toLowerCase(), dark: value.toLowerCase() })
    }
  }
  return out
}

// Bun gives the folder of this file; React Email's preview bundles it and does not, and runs in packages/mail.
const here: string | undefined = import.meta.dir
export const colorsCss = join(here ?? join(process.cwd(), 'src'), '../../ui/colors.css')

function load(): Record<TokenName, Pair> {
  const all = parseTokens(readFileSync(colorsCss, 'utf8'))
  const picked = {} as Record<TokenName, Pair>
  for (const name of tokenNames) {
    const pair = all.get(name)
    if (!pair) throw new Error(`packages/ui/colors.css has no --${name}`)
    picked[name] = pair
  }
  return picked
}

export const tokens = load()

// The fallbacks of the tokens' font stacks (--font-sans, --font-mono), without the bundled fonts, which mail
// cannot load; Segoe UI, Helvetica, Arial and Consolas are there for clients that know no generic family.
export const sans =
  "ui-sans-serif, system-ui, -apple-system, 'Segoe UI', Roboto, Helvetica, Arial, sans-serif"
export const mono = "ui-monospace, Menlo, Consolas, 'Liberation Mono', monospace"
