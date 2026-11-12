// Enforces the design system on the web app (web/), the public site (site/) and the mail (packages/mail/).
//
// Errors fail `make check`:
//   - a colour in the mail's templates (internal/domain/mail/templates) that is not the value of a token of
//     packages/ui/colors.css: mail clients have no CSS variables, so the values are copied there
//   - raw color values (hex, rgb, hsl, oklch) outside packages/ui/colors.css and the providers' marks
//     (the files directly in web/src/components/brands/: their colours are the providers', docs/design.md)
//   - em dashes used as punctuation in copy ("word — word")
// Warnings are reported but do not fail yet (TODO in docs/design.md):
//   - arbitrary Tailwind values such as p-[13px]
//   - inline style={{...}} in the web app
//
// A line containing "design-check-ignore" is skipped.

import { readdirSync, readFileSync, statSync } from 'node:fs'
import { join, relative } from 'node:path'

const root = join(import.meta.dir, '..')
const roots = [
  'web/src',
  'site/src',
  'web/index.html',
  'packages/mail/src',
  'packages/mail/scripts',
]
const extensions = ['.ts', '.tsx', '.astro', '.css', '.html', '.md', '.mdx']
const allowColorsIn = ['packages/ui/colors.css']
// The providers' marks and what their guidelines fix for their sign-in buttons. Only the files directly in
// this folder, not below it, and nowhere else in the app.
const brands = 'web/src/components/brands/'

function colorsAllowed(file: string): boolean {
  if (allowColorsIn.includes(file)) return true
  return (
    file.startsWith(brands) &&
    !file.slice(brands.length).includes('/') &&
    /\.(tsx|css)$/.test(file) &&
    !file.includes('..')
  )
}

type Finding = { file: string; line: number; rule: string; text: string }

const rules: {
  name: string
  level: 'error' | 'warn'
  test: (line: string, file: string) => boolean
}[] = [
  {
    name: 'raw color (use a token from packages/ui/colors.css)',
    level: 'error',
    test: (line, file) =>
      !colorsAllowed(file) &&
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

// The exception for the providers' marks lets nothing else through: these cases run on every check.
const cases: [file: string, line: string, fails: boolean][] = [
  ['web/src/components/brands/marks.tsx', '<rect fill="#f25022" />', false],
  ['web/src/components/brands/brands.css', '  color: #0866ff;', false],
  ['web/src/components/brands/brands.css', '  fill: light-dark(#000000, #ffffff);', false],
  ['packages/ui/colors.css', '  --ink: light-dark(#131312, #f1f1ec);', false],
  ['web/src/components/ui/buttons.tsx', '<span className="text-[#0866ff]">', true],
  ['web/src/pages/auth/ProviderSignIn.tsx', "const blue = '#0866ff'", true],
  ['web/src/components/brands/more/marks.tsx', '<rect fill="#f25022" />', true],
  ['web/src/components/brands/marks.ts.md', '<rect fill="#f25022" />', false],
  ['web/src/components/brands/../ui/buttons.tsx', '<rect fill="#f25022" />', true],
  ['web/src/components/brandsx/marks.tsx', '<rect fill="#f25022" />', true],
  ['site/src/components/brands/marks.tsx', '<rect fill="#f25022" />', true],
  ['web/src/components/brands/marks.tsx', 'style={{ color: rgb(0 0 0) }}', false],
  ['web/src/pages/settings/signin/Providers.tsx', 'fill: rgb(0 0 0)', true],
]
const color = rules[0]
const wrong = cases.filter(([file, line, fails]) => color.test(line, file) !== fails)
if (wrong.length) {
  for (const [file, line, fails] of wrong) {
    console.log(
      `design check: the colour rule ${fails ? 'lets through' : 'refuses'} ${file}: ${line}`,
    )
  }
  process.exit(1)
}

// The mail's templates are generated from packages/mail/src, which reads the tokens: every colour in them must
// still be a token's value, light or dark.
const tokenValues = new Set(
  [
    ...readFileSync(join(root, 'packages/ui/colors.css'), 'utf8').matchAll(/#[0-9a-fA-F]{3,8}\b/g),
  ].map((m) => m[0].toLowerCase()),
)
export function mailColorsWrong(line: string): string[] {
  const found = [
    ...line.matchAll(/#[0-9a-fA-F]{3,8}\b|\b(?:rgba?|hsla?|oklch|oklab|lab|lch)\([^)]*\)/g),
  ].map((m) => m[0].toLowerCase())
  return found.filter((c) => !tokenValues.has(c))
}
const mailCases: [line: string, fails: boolean][] = [
  ['style="color:#131312;border-top:1px solid #deded8"', false],
  ['.pm-ink{color:#f1f1ec!important}', false],
  ['style="color:#000000"', true],
  ['style="color:#ff0000"', true],
  ['style="color:rgb(0 0 0)"', true],
]
const mailWrong = mailCases.filter(([line, fails]) => mailColorsWrong(line).length > 0 !== fails)
if (mailWrong.length) {
  for (const [line, fails] of mailWrong) {
    console.log(
      `design check: the mail's colour rule ${fails ? 'lets through' : 'refuses'} ${line}`,
    )
  }
  process.exit(1)
}

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

const mailTemplates = 'internal/domain/mail/templates'
const mailFiles = readdirSync(join(root, mailTemplates)).filter((f) => f.endsWith('.html'))
for (const name of mailFiles) {
  const file = `${mailTemplates}/${name}`
  readFileSync(join(root, file), 'utf8')
    .split('\n')
    .forEach((text, i) => {
      for (const c of mailColorsWrong(text)) {
        findings.push({
          file,
          line: i + 1,
          rule: `error: mail colour ${c} is no token of packages/ui/colors.css`,
          text: text.trim(),
        })
      }
    })
}
files.push(...mailFiles)

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
