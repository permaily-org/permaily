import { describe, expect, test } from 'bun:test'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { gzipSync } from 'node:zlib'
import {
  checkArchive,
  checkImage,
  cpioFiles,
  eifSections,
  incomplete,
  type Manifest,
} from './gpl-source'

const bytes = (s: string) => new TextEncoder().encode(s)
const sha256 = (b: Uint8Array) => createHash('sha256').update(b).digest('hex')

function cpio(files: Record<string, string>): Uint8Array {
  const parts: Uint8Array[] = []
  let length = 0
  const push = (b: Uint8Array) => {
    parts.push(b)
    length += b.length
    const pad = (4 - (length % 4)) % 4
    if (pad) {
      parts.push(new Uint8Array(pad))
      length += pad
    }
  }
  const entry = (name: string, mode: number, data: Uint8Array) => {
    const hex = (n: number) => n.toString(16).padStart(8, '0')
    const fields = [0, mode, 0, 0, 1, 0, data.length, 0, 0, 0, 0, name.length + 1, 0]
    push(new Uint8Array([...bytes(`070701${fields.map(hex).join('')}`), ...bytes(name), 0]))
    if (data.length) push(data)
  }
  entry('dev', 0o40755, new Uint8Array())
  for (const [name, data] of Object.entries(files)) entry(name, 0o100644, bytes(data))
  entry('TRAILER!!!', 0, new Uint8Array())
  const out = new Uint8Array(length)
  let at = 0
  for (const p of parts) {
    out.set(p, at)
    at += p.length
  }
  return out
}

function eif(sections: [number, Uint8Array][]): Uint8Array {
  const headerSize = 4 + 2 + 2 + 8 + 8 + 2 + 2 + 32 * 8 + 32 * 8 + 4 + 4
  const size = headerSize + sections.reduce((n, [, d]) => n + 12 + d.length, 0)
  const out = new Uint8Array(size)
  const v = new DataView(out.buffer)
  out.set(bytes('.eif'))
  v.setUint16(26, sections.length)
  let at = headerSize
  sections.forEach(([type, data], i) => {
    v.setBigUint64(28 + i * 8, BigInt(at))
    v.setBigUint64(28 + 256 + i * 8, BigInt(data.length))
    v.setUint16(at, type)
    v.setBigUint64(at + 4, BigInt(data.length))
    out.set(data, at + 12)
    at += 12 + data.length
  })
  return out
}

const manifest = (kernel = 'kernel', init = 'init'): Manifest => ({
  components: [
    {
      name: 'kernel',
      location: 'kernel',
      sha256: sha256(bytes(kernel)),
      license: 'GPL-2.0-only',
      copyleft: true,
      source: 'unavailable',
      files: ['COPYING'],
    },
    {
      name: 'init',
      location: 'ramdisk:init',
      sha256: sha256(bytes(init)),
      license: 'Apache-2.0',
      copyleft: false,
      source: 'aws-stated',
      files: ['src/init.c'],
    },
  ],
})

const image = (boot: Record<string, string>, kernel = 'kernel') =>
  eif([
    [1, bytes(kernel)],
    [2, bytes('console=ttyS0')],
    [3, gzipSync(cpio(boot))],
    [3, gzipSync(cpio({ cmd: '/permaily-enclave', env: 'X=1', 'rootfs/permaily-enclave': 'p' }))],
  ])

describe('image', () => {
  test('reads the sections and the ramdisk files', () => {
    const sections = eifSections(image({ init: 'init' }))
    expect(sections.map((s) => s.type)).toEqual(['kernel', 'cmdline', 'ramdisk', 'ramdisk'])
    const files = cpioFiles(cpio({ 'a/b': 'xyz', c: '' }))
    expect([...files.keys()]).toEqual(['a/b', 'c'])
    expect(new TextDecoder().decode(files.get('a/b'))).toBe('xyz')
  })

  test('accepts an image whose programs are the ones the manifest describes', () => {
    expect(checkImage(image({ init: 'init' }), manifest())).toEqual([])
  })

  test('refuses a kernel that is not the one described', () => {
    const problems = checkImage(image({ init: 'init' }, 'other kernel'), manifest())
    expect(problems).toHaveLength(1)
    expect(problems[0]).toContain('kernel in the image is')
  })

  test('refuses a program the manifest does not account for', () => {
    expect(checkImage(image({ init: 'init', 'extra.ko': 'x' }), manifest())).toEqual([
      "ramdisk:extra.ko is in the image but not in the archive's manifest",
    ])
  })

  test('refuses a manifest that names a program the image does not have', () => {
    expect(checkImage(image({}), manifest())).toEqual(['init: no ramdisk:init in the image'])
  })

  test('refuses something that is not an EIF', () => {
    expect(() => checkImage(bytes('not an image at all'), manifest())).toThrow('not an EIF')
  })
})

describe('archive', () => {
  const build = (files: Record<string, string>, sums: Record<string, string>) => {
    const root = mkdtempSync(join(tmpdir(), 'gpl-source-test-'))
    for (const [f, data] of Object.entries(files)) {
      mkdirSync(join(root, f, '..'), { recursive: true })
      writeFileSync(join(root, f), data)
    }
    writeFileSync(
      join(root, 'SHA256SUMS'),
      Object.entries(sums)
        .map(([f, data]) => `${sha256(bytes(data))}  ${f}\n`)
        .join(''),
    )
    return root
  }
  const complete = {
    COPYING: 'gpl',
    'README.md': 'readme',
    'manifest.json': '{}',
    'src/init.c': 'int main',
  }

  test('accepts an archive that matches its SHA256SUMS and has every source named', () => {
    const root = build(complete, complete)
    expect(checkArchive(root, manifest())).toEqual([])
    rmSync(root, { recursive: true })
  })

  test('refuses changed, missing and unlisted files', () => {
    const root = build(
      { ...complete, 'src/init.c': 'changed', 'stray.txt': 's' },
      { ...complete, 'gone.txt': 'g' },
    )
    expect(checkArchive(root, manifest()).sort()).toEqual([
      'gone.txt is in SHA256SUMS but not in the archive',
      'src/init.c does not match SHA256SUMS',
      'stray.txt is not in SHA256SUMS',
    ])
    rmSync(root, { recursive: true })
  })

  test('refuses an archive without the source the manifest names', () => {
    const { 'src/init.c': _, ...rest } = complete
    const root = build(rest, rest)
    expect(checkArchive(root, manifest())).toEqual(['init: src/init.c is not in the archive'])
    rmSync(root, { recursive: true })
  })

  test('copyleft programs without their source are incomplete', () => {
    expect(incomplete(manifest()).map((c) => c.name)).toEqual(['kernel'])
  })
})
