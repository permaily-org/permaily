import { describe, expect, test } from 'bun:test'
import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { gzipSync } from 'node:zlib'
import { checkImage, cpioFiles, eifSections, type Inventory } from './image-inventory'

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

const inventory = (kernel = 'kernel', init = 'init'): Inventory => ({
  components: [
    {
      name: 'kernel',
      location: 'kernel',
      sha256: sha256(bytes(kernel)),
      license: 'GPL-2.0-only',
      source: 'unavailable',
    },
    {
      name: 'init',
      location: 'ramdisk:init',
      sha256: sha256(bytes(init)),
      license: 'Apache-2.0',
      source: 'aws-stated',
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

  test('accepts an image whose programs are the ones the inventory describes', () => {
    expect(checkImage(image({ init: 'init' }), inventory())).toEqual([])
  })

  test('refuses a kernel that is not the one described', () => {
    const problems = checkImage(image({ init: 'init' }, 'other kernel'), inventory())
    expect(problems).toHaveLength(1)
    expect(problems[0]).toContain('kernel in the image is')
  })

  test('refuses a program the inventory does not account for', () => {
    expect(checkImage(image({ init: 'init', 'extra.ko': 'x' }), inventory())).toEqual([
      'ramdisk:extra.ko is in the image but not in the inventory',
    ])
  })

  test('refuses an inventory that names a program the image does not have', () => {
    expect(checkImage(image({}), inventory())).toEqual(['init: no ramdisk:init in the image'])
  })

  test('refuses something that is not an EIF', () => {
    expect(() => checkImage(bytes('not an image at all'), inventory())).toThrow('not an EIF')
  })
})

test('the inventory describes each program with its license and how sure its source is', () => {
  const real: Inventory = JSON.parse(
    readFileSync(join(import.meta.dir, '../nix/aws-blobs.json'), 'utf8'),
  )
  expect(real.components.map((c) => c.location).sort()).toEqual([
    'kernel',
    'ramdisk:init',
    'ramdisk:init',
    'ramdisk:nsm.ko',
  ])
  for (const c of real.components) {
    expect(c.sha256).toMatch(/^[0-9a-f]{64}$/)
    expect(['exact', 'aws-stated', 'unavailable']).toContain(c.source)
    expect(c.license).not.toBe('')
  }
})
