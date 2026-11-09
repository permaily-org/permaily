// Enforces the design system on the web app (web/) and the public site (site/).
//
// Errors fail `make check`:
//   - raw color values (hex, rgb, hsl, oklch) outside packages/ui/colors.css
//   - em dashes used as punctuation in copy ("word — word")
// Warnings are reported but do not fail yet (TODO in docs/design.md):
//   - arbitrary Tailwind values such as p-[13px]
//   - inline style={{...}} in the web app
//
// A line containing "design-check-ignore" is skipped.

import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'

const root = join(import.meta.dir, '..')
const roots = ['web/src', 'site/src', 'web/index.html']
const extensions = ['.ts', '.tsx', '.astro', '.css', '.html', '.md', '.mdx']
const allowColorsIn = ['packages/ui/colors.css']

type Finding = { file: string; line: number; rule: string; text: string }

const rules: { name: string; level: 'error' | 'warn'; test: (line: string, file: string) => boolean }[] = [
  {
    name: 'raw color (use a token from packages/ui/colors.css)',
    level: 'error',
    test: (line, file) =>
      !allowColorsIn.includes(file) &&
      !/\.mdx?$/.test(file) &&
      /(#[0-9a-fA-F]{3,8}\b(?![-\w]))|\b(rgba?|hsla?|oklch|oklab|lab|lch)\(/.test(
        // Ignore URL fragments and anchors such as href="#top".
        line.replace(/(href|to|id)=["'{][^"'}]*["'}]/g, ''),
      ),
  },
  {
    name: 'em dash in copy (rewrite the sentence)',
    level: 'error',
    test: (line) => /\S\s?—\s?\S/.test(line),
  },
  {
    name: 'arbitrary Tailwind value (prefer a theme token)',
    level: 'warn',
    test: (line, file) => file.startsWith('web/') && /\b[a-z-]+-\[[^\]\s]+\]/.test(line),
  },
  {
    name: 'inline style (prefer a utility or a token)',
    level: 'warn',
    test: (line, file) => file.startsWith('web/') && /style=\{\{/.test(line),
  },
]

function walk(path: string, out: string[]) {
  const full = join(root, path)
  const st = statSync(full, { throwIfNoEntry: false })
  if (!st) return
  if (st.isFile()) {
    if (extensions.some((e) => path.endsWith(e))) out.push(path)
    return
  }
  for (const entry of readdirSync(full)) {
    if (entry === 'node_modules' || entry === 'dist' || entry.startsWith('.')) continue
    walk(join(path, entry), out)
  }
}

const files: string[] = []
for (const r of roots) walk(r, files)

const findings: Finding[] = []
for (const file of files) {
  const lines = readFileSync(join(root, file), 'utf8').split('\n')
  lines.forEach((text, i) => {
    if (text.includes('design-check-ignore')) return
    for (const rule of rules) {
      if (rule.test(text, relative(root, join(root, file)))) {
        findings.push({ file, line: i + 1, rule: `${rule.level}: ${rule.name}`, text: text.trim() })
      }
    }
  })
}

const errors = findings.filter((f) => f.rule.startsWith('error'))
const warnings = findings.length - errors.length
for (const f of errors) console.log(`${f.file}:${f.line} ${f.rule}\n    ${f.text}`)
console.log(
  `design check: ${files.length} files, ${errors.length} errors, ${warnings} warnings` +
    (warnings ? ' (run with --warnings to list them)' : ''),
)
if (process.argv.includes('--warnings')) {
  for (const f of findings.filter((f) => f.rule.startsWith('warn'))) {
    console.log(`${f.file}:${f.line} ${f.rule}`)
  }
}
process.exit(errors.length ? 1 : 0)
