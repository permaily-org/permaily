// Generates third-party-notices.md: every third-party component shipped in a
// Permaily artifact, its license, and the license texts.
//
//   bun scripts/third-party-notices.ts          # write the file
//   bun scripts/third-party-notices.ts --check  # fail if stale or a license is not allowed
//
// Needs `go` on PATH (for Go module metadata) and installed node_modules in web/ and site/.
// Versions are left out on purpose, so routine upgrades do not change the file.

import { execFileSync } from 'node:child_process'
import { existsSync, readdirSync, readFileSync, writeFileSync } from 'node:fs'
import { join } from 'node:path'

const root = join(import.meta.dir, '..')
const output = join(root, 'third-party-notices.md')

// Licenses compatible with distributing Permaily under its own license.
// Copyleft licenses that would cover Permaily's code (GPL, AGPL, LGPL) are not allowed.
export const ALLOWED = new Set([
  'MIT',
  'ISC',
  'BSD-2-Clause',
  'BSD-3-Clause',
  'Apache-2.0',
  '0BSD',
  'MPL-2.0',
  'OFL-1.1',
  'Unlicense',
  'CC0-1.0',
  'BlueOak-1.0.0',
  'Zlib',
])

type Component = { name: string; license: string; text: string }

// AWS's prebuilt enclave blobs (nix/aws-blobs.nix), shipped unmodified inside the
// EIF. The kernel and nsm.ko are GPL-2.0 and init links the GNU C Library (LGPL)
// statically: separate programs aggregated in the image, not linked with
// Permaily's code. Their source goes out with every release as
// permaily-gpl-source.tar.gz (nix/gpl-source.nix), and their license texts here.
const gplSource =
  'Source: permaily-gpl-source.tar.gz, attached to every release next to permaily.eif (`nix build .#gpl-source`; its README.md says, for each program, which source it holds and how certain it is). Distributed unmodified, as a separate program aggregated in the enclave image.'
const licenseText = (file: string) =>
  readFileSync(join(root, 'nix/gpl-source', file), 'utf8').trim()
const awsBlobs: Component[] = [
  {
    name: 'Linux kernel (AWS Nitro Enclaves build, aws-nitro-enclaves-cli blobs)',
    license: 'GPL-2.0-only (aggregate, unmodified)',
    text: `${gplSource}\n\n${licenseText('gpl-2.0.txt')}`,
  },
  {
    name: 'nsm.ko, Nitro Secure Module driver (aws-nitro-enclaves-cli blobs)',
    license: 'GPL-2.0-only (aggregate, unmodified)',
    text: `${gplSource}\n\n${licenseText('gpl-2.0.txt')}`,
  },
  {
    name: 'init (aws-nitro-enclaves-sdk-bootstrap)',
    license: 'Apache-2.0',
    text: 'Source: https://github.com/aws/aws-nitro-enclaves-sdk-bootstrap, and in permaily-gpl-source.tar.gz, attached to every release.',
  },
  {
    name: 'GNU C Library, statically linked into init (aws-nitro-enclaves-cli blobs)',
    license: 'LGPL-2.1-or-later (aggregate, unmodified)',
    text: `${gplSource}\n\n${licenseText('lgpl-2.1.txt')}`,
  },
]

function detectLicense(text: string): string {
  const t = text.replace(/\s+/g, ' ')
  if (/Mozilla Public License,? (Version|v\.?) ?2\.0/i.test(t)) return 'MPL-2.0'
  if (/Apache License,? Version 2\.0/i.test(t)) return 'Apache-2.0'
  if (/Permission is hereby granted, free of charge/i.test(t)) return 'MIT'
  if (
    /Permission to use, copy, modify, and\/or distribute this software for any purpose/i.test(t)
  ) {
    return /THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS/i.test(t) &&
      /copyright notice and this permission notice appear/i.test(t)
      ? 'ISC'
      : '0BSD'
  }
  if (/Redistribution and use in source and binary forms/i.test(t)) {
    return /Neither the name|names of its contributors/i.test(t) ? 'BSD-3-Clause' : 'BSD-2-Clause'
  }
  if (/SIL OPEN FONT LICENSE/i.test(t)) return 'OFL-1.1'
  if (/This is free and unencumbered software released into the public domain/i.test(t))
    return 'Unlicense'
  return 'UNKNOWN'
}

function licenseFile(dir: string): string | undefined {
  if (!existsSync(dir)) return undefined
  const file = readdirSync(dir, { withFileTypes: true }).find(
    (f) => f.isFile() && /^(licen[cs]e|copying)(\.(md|txt))?$/i.test(f.name),
  )
  return file ? readFileSync(join(dir, file.name), 'utf8').trim() : undefined
}

function goComponents(pkg: string): Component[] {
  const out = execFileSync(
    'go',
    [
      'list',
      '-deps',
      '-f',
      '{{with .Module}}{{if not .Main}}{{.Path}}\t{{.Dir}}{{end}}{{end}}',
      pkg,
    ],
    {
      cwd: root,
      encoding: 'utf8',
      env: {
        ...process.env,
        CGO_ENABLED: '0',
        GOOS: 'linux',
        GOARCH: 'arm64',
        GOFLAGS: '-mod=mod',
      },
    },
  )
  const modules = new Map<string, string>()
  for (const line of out.split('\n').filter(Boolean)) {
    const [path, dir] = line.split('\t')
    modules.set(path, dir)
  }
  const goroot = execFileSync('go', ['env', 'GOROOT'], { encoding: 'utf8' }).trim()
  const std = licenseFile(goroot) ?? ''
  const list: Component[] = [
    { name: 'Go standard library and runtime', license: detectLicense(std), text: std },
  ]
  for (const [path, dir] of [...modules].sort()) {
    const text = licenseFile(dir) ?? ''
    list.push({ name: path, license: text ? detectLicense(text) : 'MISSING', text })
  }
  return list
}

function npmComponents(project: string): Component[] {
  const base = join(root, project)
  const pkg = JSON.parse(readFileSync(join(base, 'package.json'), 'utf8'))
  const seen = new Map<string, Component>()
  const visit = (name: string, from: string) => {
    if (seen.has(name)) return
    // Resolve like Node: nested node_modules first, then the project's.
    const candidates = [join(from, 'node_modules', name), join(base, 'node_modules', name)]
    const dir = candidates.find((d) => existsSync(join(d, 'package.json')))
    if (!dir) return
    const meta = JSON.parse(readFileSync(join(dir, 'package.json'), 'utf8'))
    const declared = typeof meta.license === 'string' ? meta.license : meta.license?.type
    const text = licenseFile(dir) ?? ''
    seen.set(name, {
      name,
      license: normalizeSpdx(declared) ?? (text ? detectLicense(text) : 'MISSING'),
      text,
    })
    for (const dep of Object.keys(meta.dependencies ?? {})) visit(dep, dir)
  }
  for (const dep of Object.keys(pkg.dependencies ?? {})) visit(dep, base)
  return [...seen.values()].sort((a, b) => a.name.localeCompare(b.name))
}

function normalizeSpdx(id?: string): string | undefined {
  if (!id) return undefined
  const clean = id.replace(/^\(|\)$/g, '')
  // "MIT OR Apache-2.0": any allowed alternative is enough.
  const alternatives = clean.split(/\s+OR\s+/i)
  return alternatives.find((a) => ALLOWED.has(a)) ?? clean
}

function render(sections: { title: string; note: string; items: Component[] }[]): string {
  const lines = [
    '# Third-party notices',
    '',
    'Third-party components shipped in Permaily artifacts, and their licenses. Generated by `bun scripts/third-party-notices.ts`; do not edit by hand.',
    '',
  ]
  const texts = new Map<string, string[]>()
  for (const s of sections) {
    lines.push(`## ${s.title}`, '', s.note, '', '| Component | License |', '|---|---|')
    for (const c of s.items) {
      lines.push(`| ${c.name} | ${c.license} |`)
      if (c.text) texts.set(c.text, [...(texts.get(c.text) ?? []), c.name])
    }
    lines.push('')
  }
  lines.push('## License texts', '')
  for (const [text, names] of [...texts].sort((a, b) => a[1][0].localeCompare(b[1][0]))) {
    lines.push(`### ${[...new Set(names)].sort().join(', ')}`, '', '```text', text, '```', '')
  }
  return `${lines.join('\n').trimEnd()}\n`
}

const sections = [
  {
    title: 'Enclave image (permaily.eif)',
    note: 'The service that runs inside the AWS Nitro Enclave, including the embedded web app.',
    items: [...awsBlobs, ...goComponents('./cmd/permaily-enclave'), ...npmComponents('web')],
  },
  {
    title: 'permaily-host',
    note: 'The proxy on the parent EC2 instance.',
    items: goComponents('./cmd/permaily-host'),
  },
  {
    title: 'permaily-verify',
    note: 'The verifier CLI.',
    items: goComponents('./cmd/permaily-verify'),
  },
  {
    title: 'Public site (permaily.com)',
    note: 'Runtime dependencies of the static site.',
    items: npmComponents('site'),
  },
]

const disallowed = sections
  .flatMap((s) => s.items.map((c) => ({ ...c, section: s.title })))
  .filter((c) => !ALLOWED.has(c.license) && !c.license.includes('(aggregate, unmodified)'))
for (const c of disallowed) console.log(`not allowed: ${c.license} in ${c.name} (${c.section})`)

const content = render(sections)
if (process.argv.includes('--check')) {
  const current = existsSync(output) ? readFileSync(output, 'utf8') : ''
  if (current !== content)
    console.log('third-party-notices.md is stale: run bun scripts/third-party-notices.ts')
  process.exit(current !== content || disallowed.length ? 1 : 0)
}
writeFileSync(output, content)
console.log(
  `wrote third-party-notices.md (${sections.reduce((n, s) => n + s.items.length, 0)} components, ${disallowed.length} not allowed)`,
)
process.exit(disallowed.length ? 1 : 0)
